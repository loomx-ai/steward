package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const elasticSanSnapshotCleanup = "_elastic_san_snapshot_cleanup"
const elasticSanSnapshotCleanupProof = "_elastic_san_snapshot_cleanup_proof"

// Snapshot protocol keys and versions remain stable so existing saved executions
// survive this extension. Resource IDs and signed inventory bind the native kind.
func elasticSanIndependentChild(kind string) bool {
	return kind == elasticSanType || kind == elasticSanSnapshotType || kind == elasticSanEndpointType || kind == elasticSanVolumeType || kind == elasticSanGroupType
}

func (c *client) elasticSanChildProtection(kind string, raw map[string]any) string {
	if reason := protectionReason(resourceType{NativeType: kind}, raw); reason != "" {
		return reason
	}
	created, ok := object(raw["systemData"])["createdAt"].(string)
	if _, err := time.Parse(time.RFC3339Nano, created); !ok || err != nil {
		return "azure_elastic_san_snapshot_creation_unverified"
	}
	if raw["managedBy"] != nil && raw["managedBy"] != "" || object(raw["properties"])["managedBy"] != nil {
		return "azure_elastic_san_snapshot_managed"
	}
	if kind == elasticSanSnapshotType {
		_, sourceKind, err := parseID(text(object(object(raw["properties"])["creationData"])["sourceId"]))
		if err != nil || !strings.EqualFold(sourceKind, elasticSanVolumeType) {
			return "azure_elastic_san_snapshot_source_unverified"
		}
	} else if kind == elasticSanEndpointType {
		if _, err := c.elasticSanEndpointGroups(raw); err != nil {
			return "azure_elastic_san_endpoint_groups_unverified"
		}
		_, targetKind, err := parseID(text(object(object(raw["properties"])["privateEndpoint"])["id"]))
		if err != nil || !strings.EqualFold(targetKind, privateEndpointType) {
			return "azure_elastic_san_endpoint_target_unverified"
		}
		switch object(object(raw["properties"])["privateLinkServiceConnectionState"])["status"] {
		case "Pending", "Approved", "Rejected", "Disconnected":
		default:
			return "azure_elastic_san_endpoint_status_unverified"
		}
	} else {
		return "azure_elastic_san_child_unsupported"
	}

	switch object(raw["properties"])["provisioningState"] {
	case "Succeeded", "Failed", "Canceled", "Deleting":
		return ""
	default:
		return "azure_elastic_san_snapshot_not_ready"
	}
}

func (c *client) elasticSanChildBinding(value asset.Asset, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "elastic-san-snapshot-1", "id": value.Identity.NativeID, "connection": value.Identity.ConnectionID, "location": value.Location, "inventory": value.Normalized[elasticSanInventoryProof], "state": state})
}

func (c *client) elasticSanChildRecord(value asset.Asset) error {
	if _, err := c.elasticSanRecorded(value); err != nil {
		return err
	}
	state := object(value.Normalized[elasticSanSnapshotCleanup])
	expectedFields := 3
	if value.Identity.NativeType == elasticSanVolumeType || value.Identity.NativeType == elasticSanGroupType {
		expectedFields = 4
	}
	if value.Identity.NativeType == elasticSanType {
		expectedFields = 5
	}
	if value.ID == "" || !elasticSanIndependentChild(value.Identity.NativeType) || (value.Identity.NativeType != elasticSanVolumeType && value.Normalized["retained"] != false) || len(state) != expectedFields || text(state["resource"]) == "" || text(state["etag"]) == "" || state["protected"] != false || value.Normalized["cleanup_protected"] != false || value.Normalized[elasticSanSnapshotCleanupProof] != c.elasticSanChildBinding(value, state) {
		return serviceDenied("invalid_elastic_san_snapshot_cleanup_record")
	}
	if value.Identity.NativeType == elasticSanType {
		boundary, err := c.elasticSanBoundaryRecorded(value)
		if err != nil {
			return err
		}
		children := object(state["children"])
		if boundary["complete"] != true || state["boundary"] != value.Normalized[elasticSanBoundaryProof] || children == nil || len(children) != len(object(boundary["members"])) {
			return serviceDenied("elastic_san_root_cleanup_context_changed")
		}
		for id, entry := range children {
			child := object(entry)
			member := object(object(boundary["members"])[id])
			if len(child) != 2 || member["kind"] != child["kind"] || member["retained"] != false || text(child["configuration"]) == "" {
				return serviceDenied("elastic_san_root_cleanup_member_changed")
			}
		}
	}
	if value.Identity.NativeType == elasticSanGroupType {
		if _, err := c.elasticSanGroupRecorded(value); err != nil {
			return err
		}
		if object(value.Normalized[elasticSanGroupContext])["complete"] != true || state["group"] != value.Normalized[elasticSanGroupContextProof] {
			return serviceDenied("elastic_san_group_cleanup_context_changed")
		}
	}
	if value.Identity.NativeType == elasticSanVolumeType {
		volume := object(state["volume"])
		if len(volume) != 4 || volume["retained"] != value.Normalized["retained"] || !uuidPattern.MatchString(text(volume["volumeId"])) || object(volume["snapshots"]) == nil || object(volume["policy"]) == nil || value.Normalized["cleanup_deletion_mode"] != elasticSanVolumeMode(state) {
			return serviceDenied("elastic_san_volume_cleanup_record_changed")
		}
	}
	return nil
}

type elasticSanChildAction struct {
	client   *client
	planned  asset.Asset
	deletion catalog.RESTRequest
}

func newElasticSanChildAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*elasticSanChildAction, error) {
	if err := c.elasticSanChildRecord(value); err != nil {
		return nil, err
	}
	if connection != value.Identity.ConnectionID || kind.NativeType != value.Identity.NativeType || !elasticSanIndependentChild(kind.NativeType) || kind.ReadOnly {
		return nil, serviceDenied("elastic_san_snapshot_action_changed")
	}
	op, params, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	if value.Identity.NativeType == elasticSanVolumeType && value.Normalized["retained"] == true {
		params["deleteType"] = "permanent"
	}
	deletion, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	return &elasticSanChildAction{client: c, planned: value, deletion: deletion}, nil
}

func (*elasticSanChildAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }

func (a *elasticSanChildAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.ExecutionResult, request.IdempotencyKey = nil, ""
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "elastic-san-snapshot-delete-1", "request": request, "origin": result.ProviderOperationID, "data": data})
}

func (a *elasticSanChildAction) identity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID != a.planned.ID || request.Asset.Identity != a.planned.Identity || request.Asset.Location != a.planned.Location || request.Asset.Normalized[elasticSanSnapshotCleanupProof] != a.planned.Normalized[elasticSanSnapshotCleanupProof] {
		return serviceDenied("elastic_san_snapshot_request_changed")
	}
	if a.planned.Identity.NativeType == elasticSanType {
		if err := a.rootRequest(request); err != nil {
			return err
		}
	} else if a.planned.Identity.NativeType == elasticSanVolumeType {
		if err := a.volumeRequest(request); err != nil {
			return err
		}
	} else if a.planned.Identity.NativeType == elasticSanGroupType {
		if err := a.groupRequest(request); err != nil {
			return err
		}
	} else if len(request.Parameters)+len(request.LifecycleImpacts)+len(request.PrerequisiteDeletions) != 0 {
		return serviceDenied("elastic_san_child_request_changed")
	}
	if err := a.client.elasticSanChildRecord(request.Asset); err != nil {
		return err
	}
	if result := request.ExecutionResult; result != nil {
		if (len(result.Data) != 3 && !((a.planned.Identity.NativeType == elasticSanVolumeType || a.planned.Identity.NativeType == elasticSanGroupType) && len(result.Data) == 4 && object(result.Data["outcome"]) != nil)) || result.Data["phase"] != "delete" || result.Data["binding"] != a.phaseBinding(request, *result) {
			return serviceDenied("elastic_san_snapshot_phase_changed")
		}
		return a.client.elasticSanVerifyReceipt(a.planned.Identity.NativeID, a.planned.Location, object(result.Data["operation"]))
	}
	return nil
}

func (a *elasticSanChildAction) observe(ctx context.Context) (map[string]any, error) {
	if a.planned.Identity.NativeType == elasticSanGroupType {
		raw, _, err := a.groupObservation(ctx)
		return raw, err
	}
	if a.planned.Identity.NativeType == elasticSanVolumeType {
		raw, _, err := a.volumeObservation(ctx)
		return raw, err
	}
	res, err := a.client.elasticSanRead(ctx, a.planned.Identity.NativeID, a.planned.Identity.NativeType)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.client.privateConfiguration(elasticSanChildSnapshot(a.planned.Identity.NativeType, res.data)) != object(a.planned.Normalized[elasticSanSnapshotCleanup])["resource"] || text(res.data["location"]) != "" && !strings.EqualFold(text(res.data["location"]), a.planned.Location) {
		return nil, serviceDenied("elastic_san_snapshot_configuration_changed")
	}
	return res.data, nil
}

func (a *elasticSanChildAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	for range 2 {
		raw, err := a.observe(ctx)
		if err != nil {
			return check, err
		}
		if raw == nil {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if object(raw["properties"])["provisioningState"] == "Deleting" || a.planned.Identity.NativeType == elasticSanGroupType && object(raw["properties"])["provisioningState"] == "SoftDeleting" {
			return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"elastic_san_wait": true}}, nil
		}
		if a.planned.Identity.NativeType == elasticSanType {
			if err := a.rootPreflight(ctx, raw); err != nil {
				return check, err
			}
		} else if a.planned.Identity.NativeType == elasticSanVolumeType {
			if err := a.volumePreflight(ctx, raw); err != nil {
				return check, err
			}
		} else if a.planned.Identity.NativeType == elasticSanGroupType {
			if err := a.groupPreflight(ctx, raw); err != nil {
				return check, err
			}
		} else if reason := a.client.elasticSanChildProtection(a.planned.Identity.NativeType, raw); reason != "" {
			return check, serviceDenied(reason)
		}
		if a.planned.Identity.NativeType != elasticSanType && a.client.privateConfiguration(elasticSanChildVersion(a.planned.Identity.NativeType, raw)) != object(a.planned.Normalized[elasticSanSnapshotCleanup])["etag"] {
			return check, serviceDenied("elastic_san_snapshot_etag_changed")
		}
		parents := []string{elasticSanRoot(a.planned.Identity.NativeID)}
		if a.planned.Identity.NativeType == elasticSanSnapshotType || a.planned.Identity.NativeType == elasticSanVolumeType {
			parents = append(parents, elasticSanParent(a.planned.Identity.NativeID, a.planned.Identity.NativeType))
		} else if a.planned.Identity.NativeType == elasticSanEndpointType {
			groups, err := a.client.elasticSanEndpointGroups(raw)
			if err != nil {
				return check, err
			}
			parents = append(parents, groups...)
		}
		for _, parentID := range parents {
			_, parentKind, _ := parseID(parentID)
			parent := struct{ id, kind string }{parentID, elasticSanKind(parentKind)}
			res, err := a.client.elasticSanRead(ctx, parent.id, parent.kind)
			if isNotFound(err) {
				return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"elastic_san_wait": true}}, nil
			}
			if err != nil {
				return check, err
			}
			if parent.kind == elasticSanType && resourceRegion(res.data) != a.planned.Location || text(res.data["location"]) != "" && !strings.EqualFold(text(res.data["location"]), a.planned.Location) {
				return check, serviceDenied("elastic_san_snapshot_parent_region_changed")
			}
			switch object(res.data["properties"])["provisioningState"] {
			case "Deleting", "Deleted", "SoftDeleting":
				return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"elastic_san_wait": true}}, nil
			}
			if reason := protectionReason(resourceType{NativeType: parent.kind}, res.data); reason != "" {
				return check, serviceDenied(reason)
			}
		}
		groupID := strings.Join(strings.Split(a.planned.Identity.NativeID, "/")[:5], "/")
		group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
		if err != nil {
			return check, err
		}
		if !insightsARMReadValid(group, groupID, groupType) {
			return check, serviceDenied("elastic_san_snapshot_resource_group_unverified")
		}
		if reason := protectionReason(resourceType{NativeType: groupType}, group.data); reason != "" {
			return check, serviceDenied(reason)
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return check, err
		}
		for _, id := range append(parents, a.planned.Identity.NativeID) {
			if locked(id, locks) {
				return check, serviceDenied("azure_management_lock")
			}
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *elasticSanChildAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil {
		return *request.ExecutionResult, nil
	}
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	id, location := a.planned.Identity.NativeID, a.planned.Location
	res := response{}
	receipt := a.client.elasticSanSignReceipt(id, location, nil)
	if !check.Absent && check.Evidence["elastic_san_wait"] != true {
		headers := maps.Clone(a.deletion.Headers)
		if headers == nil {
			headers = map[string]string{}
		}
		if a.planned.Identity.NativeType == elasticSanVolumeType {
			headers["x-ms-delete-snapshots"] = "false"
			headers["x-ms-force-delete"] = "false"
			if request.Parameters["force_delete"] == true {
				headers["x-ms-force-delete"] = "true"
			}
		}
		if request.IdempotencyKey != "" {
			headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
		}
		res, err = a.client.requestBody(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers)
		if err != nil && !isNotFound(err) {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
		if err == nil {
			receipt, err = a.client.elasticSanDeleteReceipt(id, location, res)
			if err != nil {
				return contracts.ActionResult{}, err
			}
		}
	}
	origin := res.requestID
	if endpoint := text(receipt["url"]); endpoint != "" {
		origin, _ = a.client.elasticSanPollURL(id, location, endpoint)
	}
	result := contracts.ActionResult{ProviderOperationID: origin, ProviderRequestID: res.requestID, RetryAfter: retryAfter(res.header), Data: map[string]any{"phase": "delete", "operation": receipt}}
	result.Data["binding"] = a.phaseBinding(request, result)
	return result, nil
}

func (a *elasticSanChildAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.planned.Identity.NativeType == elasticSanType {
		raw, err := a.observe(ctx)
		if err == nil && raw == nil {
			err = a.rootChildrenFinished(ctx, true)
		}
		return contracts.ReadbackResult{Exists: raw != nil, State: text(object(raw["properties"])["provisioningState"])}, contracts.DependencyReadError(err)
	}
	if a.planned.Identity.NativeType == elasticSanGroupType {
		_, out, err := a.groupObservation(ctx)
		if err == nil && !out.Exists {
			err = a.groupChildrenFinished(ctx, true)
		}
		return out, contracts.DependencyReadError(err)
	}
	if a.planned.Identity.NativeType == elasticSanVolumeType {
		_, out, err := a.volumeObservation(ctx)
		if err == nil && !out.Exists {
			state := object(a.planned.Normalized[elasticSanSnapshotCleanup])
			children, readErr := a.client.elasticSanVolumeSnapshots(ctx, a.planned.Identity.NativeID, object(object(state["volume"])["snapshots"]))
			if readErr != nil {
				err = readErr
			} else if len(children) != 0 {
				err = serviceDenied("elastic_san_volume_snapshot_remains")
			}
		}
		return out, contracts.DependencyReadError(err)
	}
	raw, err := a.observe(ctx)
	return contracts.ReadbackResult{Exists: raw != nil, State: text(object(raw["properties"])["provisioningState"])}, contracts.DependencyReadError(err)
}

func (a *elasticSanChildAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	poll, err := a.client.elasticSanPoll(ctx, a.planned.Identity.NativeID, a.planned.Location, object(result.Data["operation"]))
	if err != nil {
		if isNotFound(err) {
			// An expired callback can be superseded only by the selected child's own
			// independently verified absence, never by a missing parent/index.
			read, readErr := a.Readback(ctx, request)
			if readErr == nil && !read.Exists {
				data := maps.Clone(result.Data)
				if a.planned.Identity.NativeType == elasticSanVolumeType || a.planned.Identity.NativeType == elasticSanGroupType {
					data["outcome"] = read.Data
					result.Data = data
					data["binding"] = a.phaseBinding(request, result)
				}
				return contracts.WaitResult{Done: true, Data: data}, nil
			}
		}
		return contracts.WaitResult{}, contracts.DependencyReadError(err)
	}
	out := contracts.WaitResult{State: poll.State, RetryAfter: max(2*time.Second, poll.RetryAfter), Data: maps.Clone(result.Data)}
	out.Data["operation"] = poll.Data
	result.Data = out.Data
	out.Data["binding"] = a.phaseBinding(request, result)
	if !poll.Done {
		return out, nil
	}
	read, err := a.Readback(ctx, request)
	out.Done, out.State = err == nil && !read.Exists, read.State
	if err == nil && out.Done && (a.planned.Identity.NativeType == elasticSanVolumeType || a.planned.Identity.NativeType == elasticSanGroupType) {
		out.Data["outcome"] = read.Data
		result.Data = out.Data
		out.Data["binding"] = a.phaseBinding(request, result)
	}
	return out, err
}

var _ contracts.ActionDriver = (*elasticSanChildAction)(nil)
