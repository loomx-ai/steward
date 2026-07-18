package alicloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const (
	ACKClusterNativeType       = "ACS::ACK::Cluster"
	AliKafkaInstanceNativeType = "ACS::AliKafka::Instance"
	ARMSEnvironmentNativeType  = "ACS::ARMS::Environment"
	ROSStackNativeType         = "ACS::ROS::Stack"
	ROSStackGroupNativeType    = "ACS::ROS::StackGroup"
	PrometheusNativeType       = "ACS::ARMS::Prometheus"
	KMSKeyNativeType           = "ACS::KMS::Key"
	SLSProjectNativeType       = "ACS::SLS::Project"
	NASFileSystemNativeType    = "ACS::NAS::FileSystem"
	ECSImageNativeType         = "ACS::ECS::Image"
	OSSBucketNativeType        = "ACS::OSS::Bucket"
)

const actionReadbackInterval = 2 * time.Second

const KMSDeletionScheduledState = "scheduled_deletion"

const (
	slsGetProjectOperation                     = "AlibabaCloud.SLS.GetProject"
	slsUpdateProjectOperation                  = "AlibabaCloud.SLS.UpdateProject"
	armsListEnvironmentFeaturesOperation       = "AlibabaCloud.ARMS.ListEnvironmentFeatures"
	armsDeleteEnvironmentFeatureOperation      = "AlibabaCloud.ARMS.DeleteEnvironmentFeature"
	armsFeatureUninstallPhase                  = "feature_uninstall"
	ecsDescribeImageSharePermissionOperation   = "DescribeImageSharePermission"
	ecsModifyImageSharePermissionOperation     = "ModifyImageSharePermission"
	imageVisibilityChangePhase                 = "image_visibility_change"
	imageSharePermissionChangePhase            = "image_share_permission_change"
	privateLinkListConnectionsOperation        = "AlibabaCloud.PrivateLink.ListVpcEndpointConnections"
	privateLinkDisableConnectionOperation      = "AlibabaCloud.PrivateLink.DisableVpcEndpointConnection"
	privateLinkDisableZoneConnectionOperation  = "AlibabaCloud.PrivateLink.DisableVpcEndpointZoneConnection"
	privateLinkListServicesOperation           = "AlibabaCloud.PrivateLink.ListVpcEndpointServices"
	privateLinkListResourcesOperation          = "AlibabaCloud.PrivateLink.ListVpcEndpointServiceResources"
	privateLinkDetachResourceOperation         = "AlibabaCloud.PrivateLink.DetachResourceFromVpcEndpointService"
	privateLinkDisconnectBoundZonePhase        = "disconnect_bound_endpoint_zone"
	privateLinkDetachBoundResourcePhase        = "detach_bound_service_resource"
	privateLinkBoundResourceDeletePhase        = "bound_resource_delete_requested"
	rosListStackInstancesOperation             = "AlibabaCloud.ROS.ListStackInstances"
	rosDeleteStackInstancesOperation           = "AlibabaCloud.ROS.DeleteStackInstances"
	rosGetStackGroupOperation                  = "AlibabaCloud.ROS.GetStackGroupOperation"
	rosListStackGroupOperationResultsOperation = "AlibabaCloud.ROS.ListStackGroupOperationResults"
	rosDeleteStackInstancesPhase               = "delete_stack_instances"
	rosDeleteStackGroupRequestedPhase          = "stack_group_delete_requested"
	ossGetBucketInfoOperation                  = "AlibabaCloud.OSS.GetBucketInfo"
	ossGetBucketVersioningOperation            = "AlibabaCloud.OSS.GetBucketVersioning"
	ossPutBucketVersioningOperation            = "AlibabaCloud.OSS.PutBucketVersioning"
	ossBucketNotFoundEnvelopeCode              = "0015-00000101"
	ossGetBucketHDFSConfigOperation            = "AlibabaCloud.OSS.GetBucketHDFSConfig"
	ossPutBucketHDFSConfigOperation            = "AlibabaCloud.OSS.PutBucketHDFSConfig"
	ossListBucketHDFSFilesOperation            = "AlibabaCloud.OSS.ListBucketHDFSFiles"
	ossDeleteBucketHDFSFileOperation           = "AlibabaCloud.OSS.DeleteBucketHDFSFile"
	ossDeleteDataLakeBucketOperation           = "AlibabaCloud.OSS.DeleteDataLakeBucket"
	ossListObjectVersionsOperation             = "AlibabaCloud.OSS.ListObjectVersions"
	ossListObjectsOperation                    = "AlibabaCloud.OSS.ListObjects"
	ossDeleteMultipleObjectsOperation          = "AlibabaCloud.OSS.DeleteMultipleObjects"
	ossDeleteObjectOperation                   = "AlibabaCloud.OSS.DeleteObject"
	ossListMultipartUploadsOperation           = "AlibabaCloud.OSS.ListMultipartUploads"
	ossAbortMultipartUploadOperation           = "AlibabaCloud.OSS.AbortMultipartUpload"
	ossListLiveChannelOperation                = "AlibabaCloud.OSS.ListLiveChannel"
	ossDeleteLiveChannelOperation              = "AlibabaCloud.OSS.DeleteLiveChannel"
	ossDeleteObjectVersionsPhase               = "delete_object_versions"
	ossDeleteCurrentObjectsPhase               = "delete_current_objects"
	ossAbortMultipartUploadsPhase              = "abort_multipart_uploads"
	ossDeleteLiveChannelsPhase                 = "delete_live_channels"
	ossRetryDeleteBucketPhase                  = "retry_delete_bucket"
	ossPauseHDFSPhase                          = "pause_hdfs"
	ossWaitHDFSPausedPhase                     = "wait_hdfs_paused"
	ossDeleteHDFSFilesPhase                    = "delete_hdfs_files"
	ossRestoreHDFSPhase                        = "restore_hdfs"
	ossWaitHDFSRestoredPhase                   = "wait_hdfs_restored"
	// These phase names are retained so attempts persisted by older builds can
	// resume through the new HDFS safe-mode workflow.
	ossDisableDataLakeStoragePhase = "disable_data_lake_storage"
	// ossWaitDataLakeCleanupPhase is retained so cleanup attempts persisted by
	// older builds resume by disabling the service automatically.
	ossWaitDataLakeCleanupPhase      = "wait_data_lake_cleanup"
	ossBucketDeleteRequestedPhase    = "bucket_delete_requested"
	vpcDetachDhcpOptionsSetOperation = "AlibabaCloud.DetachDhcpOptionsSetFromVpc"
	vpcDeleteDhcpOptionsSetOperation = "AlibabaCloud.DeleteDhcpOptionsSet"
	vpcListDhcpOptionsSetsOperation  = "AlibabaCloud.ListDhcpOptionsSets"
	vpcDetachDhcpOptionsSetPhase     = "detach_dhcp_options_set"
	vpcDeleteDhcpOptionsSetPhase     = "delete_dhcp_options_set"
)

const (
	ossBucketCleanupPageSize       = 1000
	ossBucketCleanupMaxPass        = 3
	ossBucketCleanupPollInterval   = 250 * time.Millisecond
	ossDataLakeCleanupPollInterval = 5 * time.Second
	ossHDFSSafeModeConfigName      = "namespace.safemode.enable"
	ossHDFSProtocolClientVersion   = "6.13.2"
	ossHDFSProtocolUser            = "steward"
)

// ResourceAction is bound to one connection credential and region by the
// cleanup worker. It implements live preflight, delete, waiter, and readback
// without exposing those credentials in the ActionRequest domain object.
type ResourceAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
	nativeType   string
	action       spec.ActionSpec
}

func NewActionHook(provider contracts.Provider, connectionID asset.ConnectionID, region, nativeType string) (*ResourceAction, error) {
	bundle, err := LoadBundle()
	if err != nil {
		return nil, err
	}
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return newSpecAction(provider, connectionID, region, compiled)
		}
	}
	return nil, fmt.Errorf("Alibaba Cloud native type %q has no resource spec", nativeType)
}

func newSpecAction(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
	compiled spec.CompiledSpec,
) (*ResourceAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud action hook requires connection ID and region")
	}
	action, ok := compiled.Definition.Actions["delete"]
	if !ok {
		return nil, fmt.Errorf("Alibaba Cloud native type %q has no delete action", compiled.ResourceKind.NativeType)
	}
	if action.Read == nil || (action.Waiter != "absent" && action.Waiter != "terminal") {
		return nil, fmt.Errorf(
			"Alibaba Cloud native type %q requires a code hook for its delete action",
			compiled.ResourceKind.NativeType,
		)
	}
	return &ResourceAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
		nativeType: compiled.ResourceKind.NativeType, action: action,
	}, nil
}

func (h *ResourceAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := h.validate(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	if _, err := h.actionParameters(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	readback, result, resource, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !readback.Exists {
		return contracts.PreflightResult{Absent: true, Reason: "resource no longer exists", Evidence: map[string]any{"provider_request_id": result.RequestID}}, nil
	}
	if deletionInProgressState(readback.State) {
		return contracts.PreflightResult{
			Absent: true,
			Reason: "resource deletion is already in progress",
			Evidence: map[string]any{
				"provider_request_id": result.RequestID, "state": readback.State,
			},
		}, nil
	}
	if h.terminalState(readback.State) {
		return contracts.PreflightResult{
			Absent: true,
			Reason: "resource is already in a terminal state",
			Evidence: map[string]any{
				"provider_request_id": result.RequestID, "state": readback.State,
			},
		}, nil
	}
	for _, precondition := range h.action.Preconditions {
		actual := strings.TrimSpace(stringValue(valueAtPath(resource, precondition.Path)))
		if !matchesAllowedValue(actual, precondition.AllowedValues) {
			return contracts.PreflightResult{
				Allowed: false,
				Reason:  precondition.Reason,
				Evidence: map[string]any{
					"provider_request_id": result.RequestID,
					"native_id":           request.Asset.Identity.NativeID,
					"path":                precondition.Path,
					"actual_value":        actual,
					"allowed_values":      append([]string(nil), precondition.AllowedValues...),
				},
			}, nil
		}
	}
	evidence := map[string]any{
		"provider_request_id": result.RequestID, "native_id": request.Asset.Identity.NativeID,
		"state": readback.State,
	}
	if h.nativeType == ECSImageNativeType && boolValue(resource["IsPublic"]) {
		evidence["pre_delete_action"] = "make_private"
		evidence["image_visibility"] = "public"
	}
	return contracts.PreflightResult{
		Allowed:  true,
		Evidence: evidence,
	}, nil
}

func (h *ResourceAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := h.validate(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if err := h.unsupportedCleanupError(request.Asset); err != nil {
		return contracts.ActionResult{}, err
	}
	if h.nativeType == SLSProjectNativeType {
		return h.deleteSLSProject(ctx, request)
	}
	if h.nativeType == ARMSEnvironmentNativeType {
		return h.deleteARMSEnvironment(ctx, request)
	}
	if h.nativeType == ROSStackGroupNativeType {
		return h.deleteROSStackGroup(ctx, request)
	}
	if h.nativeType == endpointServiceNativeType {
		return h.deletePrivateLinkEndpointService(ctx, request)
	}
	if h.nativeType == NASFileSystemNativeType &&
		!strings.EqualFold(normalizedActionValue(request.Asset.Normalized, "FileSystemType"), "extreme") {
		if err := h.deleteNASLifecyclePolicies(ctx, request); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	if h.nativeType == DataWorksProjectNativeType {
		readback, result, _, err := h.readbackDetails(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if !readback.Exists {
			return contracts.ActionResult{
				Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
			}, nil
		}
		if deletionInProgressState(readback.State) {
			return deletionInProgressResult(readback.State, result.RequestID), nil
		}
	}
	if h.nativeType == diskNativeType {
		readback, _, resource, err := h.readbackDetails(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if !readback.Exists {
			return contracts.ActionResult{
				Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
			}, nil
		}
		instanceID := strings.TrimSpace(stringValue(resource["InstanceId"]))
		if strings.EqualFold(strings.TrimSpace(stringValue(resource["Type"])), "data") && instanceID != "" {
			return h.detachDataDisk(ctx, request, instanceID)
		}
	}
	if h.nativeType == vpcNativeType {
		detachResult, handled, err := h.detachVPCDhcpOptionsSet(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if handled {
			return detachResult, nil
		}
	}
	imagePreparation, err := h.makeImagePrivateIfPublic(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !imagePreparation.exists {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, nil
	}
	if imagePreparation.changed {
		return contracts.ActionResult{
			ProviderRequestID: imagePreparation.requestID,
			Data: map[string]any{
				"phase":                 imageVisibilityChangePhase,
				"image_made_private":    true,
				"visibility_request_id": imagePreparation.requestID,
			},
			RetryAfter: h.pollInterval(),
		}, nil
	}
	sharePreparation, err := h.removeImageShareAccounts(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !sharePreparation.exists {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, nil
	}
	if sharePreparation.changed {
		return imageSharePermissionChangeResult(sharePreparation, h.pollInterval()), nil
	}
	preparation, err := h.prepareDeletion(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !preparation.exists {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, nil
	}
	if preparation.inProgress {
		result := deletionInProgressResult(
			preparation.state,
			preparation.readRequestID,
		)
		result.ProviderRequestID = preparation.readRequestID
		if h.nativeType == KMSKeyNativeType {
			for name, value := range preparation.data {
				result.Data[name] = value
			}
			result.RetryAfter = 0
		}
		return result, nil
	}
	if _, boundResource := privateLinkServiceResourceType(h.nativeType); boundResource {
		result, handled, err := h.advancePrivateLinkServiceResourceCleanup(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if handled {
			return result, nil
		}
	}
	var ossVersioningData map[string]any
	if h.nativeType == OSSBucketNativeType {
		var exists bool
		ossVersioningData, exists, err = h.suspendOSSBucketVersioning(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if !exists {
			return contracts.ActionResult{
				Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
			}, nil
		}
	}
	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    operation, Scope: map[string]string{"region": h.region},
		Parameters: parameters, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		if h.nativeType == OSSBucketNativeType {
			if providerError, dataLake := ossDataLakeCleanupTrigger(err); dataLake {
				return h.startOSSDataLakeStorageDisable(ossVersioningData, providerError), nil
			}
			if providerError, cleanup := ossBucketCleanupTrigger(err); cleanup {
				return h.startOSSBucketCleanup(ossVersioningData, providerError), nil
			}
		}
		if h.nativeType == DataWorksProjectNativeType {
			if requestID, ok := dataWorksProjectDeletionInProgress(err); ok {
				accepted := deletionInProgressResult("Deleting", "")
				accepted.ProviderRequestID = requestID
				return accepted, nil
			}
		}
		if isProviderErrorCategory(err, execution.ErrorRetryable) {
			readback, readResult, _, readErr := h.readbackDetails(ctx, request)
			if readErr == nil {
				switch {
				case !readback.Exists:
					return contracts.ActionResult{
						Data: map[string]any{
							"phase":               "absent",
							"readback_request_id": readResult.RequestID,
						},
						RetryAfter: h.pollInterval(),
					}, nil
				case deletionInProgressState(readback.State):
					return deletionInProgressResult(readback.State, readResult.RequestID), nil
				}
			}
		}
		return contracts.ActionResult{}, err
	}
	if h.nativeType == DataWorksResourceGroupNativeType &&
		dataWorksResourceGroupAlreadyDeleted(result.Data) {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "704203",
			Message:   strings.TrimSpace(stringValue(result.Data["Message"])),
			RequestID: result.RequestID,
			Summary:   map[string]any{"operation": operation},
		}}
	}
	retryAfter := h.pollInterval()
	if h.nativeType == KMSKeyNativeType {
		retryAfter = 0
	}
	data := cloneTopologyMap(result.Data)
	if len(ossVersioningData) > 0 {
		if data == nil {
			data = make(map[string]any)
		}
		for name, value := range ossVersioningData {
			data[name] = value
		}
	}
	if preparation.protectionDisabled {
		if data == nil {
			data = make(map[string]any)
		}
		data["deletion_protection_disabled"] = true
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID, Data: data,
		RetryAfter: retryAfter,
	}, nil
}

func (h *ResourceAction) suspendOSSBucketVersioning(
	ctx context.Context,
	request contracts.ActionRequest,
) (map[string]any, bool, error) {
	parameters := h.ossBucketParameters(request)
	versioning, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossGetBucketVersioningOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if isNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	status, err := ossBucketVersioningStatus(versioning.Data)
	if err != nil {
		return nil, false, fmt.Errorf(
			"Alibaba Cloud OSS GetBucketVersioning response: %w",
			err,
		)
	}
	data := map[string]any{
		"get_bucket_versioning_request_id": versioning.RequestID,
		"versioning_status_before":         status,
	}
	if status != "Enabled" {
		return data, true, nil
	}
	parameters = h.ossBucketParameters(request)
	parameters["VersioningConfiguration"] = map[string]any{"Status": "Suspended"}
	suspended, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossPutBucketVersioningOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
		IdempotencyKey: request.IdempotencyKey +
			":suspend-versioning",
	})
	if err != nil {
		return nil, false, err
	}
	data["versioning_suspended"] = true
	data["put_bucket_versioning_request_id"] = suspended.RequestID
	return data, true, nil
}

func ossBucketVersioningStatus(data map[string]any) (string, error) {
	root := data
	if value, exists := data["VersioningConfiguration"]; exists {
		var ok bool
		root, ok = productAPIResourceMap(value)
		if !ok {
			if nilLikeValue(value) || strings.TrimSpace(stringValue(value)) == "" {
				return "Unversioned", nil
			}
			return "", errors.New("VersioningConfiguration is not an object")
		}
	} else if _, flattened := data["Status"]; !flattened {
		if len(data) == 0 {
			return "Unversioned", nil
		}
		return "", errors.New("missing VersioningConfiguration")
	}
	status := strings.TrimSpace(stringValue(root["Status"]))
	switch {
	case status == "":
		return "Unversioned", nil
	case strings.EqualFold(status, "Enabled"):
		return "Enabled", nil
	case strings.EqualFold(status, "Suspended"):
		return "Suspended", nil
	default:
		return "", fmt.Errorf("unsupported versioning status %q", status)
	}
}

func ossBucketCleanupTrigger(err error) (execution.ProviderError, bool) {
	providerError, ok := ossProviderError(err)
	if !ok {
		return execution.ProviderError{}, false
	}
	return providerError, strings.EqualFold(strings.TrimSpace(providerError.Code), "BucketNotEmpty")
}

func ossDataLakeCleanupTrigger(err error) (execution.ProviderError, bool) {
	providerError, ok := ossProviderError(err)
	if !ok {
		return execution.ProviderError{}, false
	}
	code := strings.TrimSpace(providerError.Code)
	message := strings.ToLower(strings.TrimSpace(providerError.Message))
	return providerError,
		strings.EqualFold(code, "AccessDenied") &&
			strings.Contains(message, "data lake storage is disabled")
}

func ossProviderError(err error) (execution.ProviderError, bool) {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) {
		return execution.ProviderError{}, false
	}
	return providerCall.Provider, true
}

func (h *ResourceAction) startOSSBucketCleanup(
	state map[string]any,
	trigger execution.ProviderError,
) contracts.ActionResult {
	data := cloneTopologyMap(state)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = ossDeleteObjectVersionsPhase
	data["cleanup_pass"] = 1
	data["trigger_code"] = trigger.Code
	data["trigger_message"] = trigger.Message
	return contracts.ActionResult{
		ProviderRequestID: trigger.RequestID,
		Data:              data,
		RetryAfter:        ossBucketCleanupPollInterval,
	}
}

func (h *ResourceAction) startOSSDataLakeStorageDisable(
	state map[string]any,
	trigger execution.ProviderError,
) contracts.ActionResult {
	data := cloneTopologyMap(state)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = ossPauseHDFSPhase
	data["data_lake_storage_detected"] = true
	data["hdfs_safe_mode_target"] = true
	data["hdfs_cleanup_automatic"] = true
	data["hdfs_restored"] = false
	data["hdfs_restore_requested"] = false
	delete(data, "data_lake_storage_target_status")
	delete(data, "data_lake_storage_disable_automatic")
	delete(data, "manual_action_required")
	delete(data, "data_lake_cleanup_steps")
	delete(data, "data_lake_cleanup_command")
	data["trigger_code"] = trigger.Code
	data["trigger_message"] = trigger.Message
	return contracts.ActionResult{
		ProviderRequestID: trigger.RequestID,
		Data:              data,
		RetryAfter:        ossBucketCleanupPollInterval,
	}
}

func (h *ResourceAction) pauseOSSHDFS(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	result, err := h.putOSSHDFSSafeMode(ctx, request, true, "pause-hdfs")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = ossWaitHDFSPausedPhase
	data["hdfs_safe_mode_requested"] = true
	data["hdfs_pause_request_id"] = result.RequestID
	delete(data, "manual_action_required")
	delete(data, "data_lake_cleanup_steps")
	delete(data, "data_lake_cleanup_command")
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID,
		Data:              data,
		RetryAfter:        ossDataLakeCleanupPollInterval,
	}, nil
}

func (h *ResourceAction) putOSSHDFSSafeMode(
	ctx context.Context,
	request contracts.ActionRequest,
	enabled bool,
	idempotencySuffix string,
) (contracts.InvocationResult, error) {
	parameters := h.ossHDFSConfigParameters(request, "putConfig")
	parameters["request"].(map[string]any)["parameters"] = map[string]any{
		"NamespaceConfiguration": map[string]any{
			"property": map[string]any{
				"name": ossHDFSSafeModeConfigName, "value": strconv.FormatBool(enabled),
			},
		},
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   h.connectionID,
		Operation:      ossPutBucketHDFSConfigOperation,
		Scope:          map[string]string{"region": h.region},
		Parameters:     parameters,
		IdempotencyKey: request.IdempotencyKey + ":" + idempotencySuffix,
	})
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if err := ossHDFSApplicationError(result.Data); err != nil {
		return contracts.InvocationResult{}, err
	}
	return result, nil
}

func (h *ResourceAction) restoreOSSHDFSAfterFailure(
	ctx context.Context,
	request contracts.ActionRequest,
	cause error,
) error {
	_, restoreErr := h.putOSSHDFSSafeMode(ctx, request, false, "restore-hdfs-after-failure")
	if restoreErr == nil {
		return cause
	}
	return errors.Join(cause, fmt.Errorf(
		"restore Alibaba Cloud OSS HDFS after cleanup failure: %w", restoreErr,
	))
}

func (h *ResourceAction) waitOSSHDFSPaused(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	paused, result, err := h.getOSSHDFSSafeMode(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	data["hdfs_safe_mode"] = paused
	data["hdfs_safe_mode_read_request_id"] = result.RequestID
	if paused {
		data["phase"] = ossDeleteHDFSFilesPhase
		return ossBucketCleanupResult(data, result.RequestID), nil
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID,
		Data:              data,
		RetryAfter:        ossDataLakeCleanupPollInterval,
	}, nil
}

func (h *ResourceAction) getOSSHDFSSafeMode(
	ctx context.Context,
	request contracts.ActionRequest,
) (bool, contracts.InvocationResult, error) {
	parameters := h.ossHDFSConfigParameters(request, "getConfig")
	parameters["request"].(map[string]any)["parameters"] = map[string]any{
		"NamespaceConfiguration": map[string]any{
			"name": ossHDFSSafeModeConfigName,
		},
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossGetBucketHDFSConfigOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if err != nil {
		return false, contracts.InvocationResult{}, err
	}
	if err := ossHDFSApplicationError(result.Data); err != nil {
		return false, contracts.InvocationResult{}, err
	}
	paused, err := ossHDFSSafeModeEnabled(result.Data)
	if err != nil {
		return false, contracts.InvocationResult{}, fmt.Errorf(
			"Alibaba Cloud OSS HDFS getConfig response: %w", err,
		)
	}
	return paused, result, nil
}

func (h *ResourceAction) restoreOSSHDFS(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	result, err := h.putOSSHDFSSafeMode(ctx, request, false, "restore-hdfs")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	data["phase"] = ossWaitHDFSRestoredPhase
	data["hdfs_restore_requested"] = true
	data["hdfs_restore_request_id"] = result.RequestID
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID,
		Data:              data,
		RetryAfter:        ossDataLakeCleanupPollInterval,
	}, nil
}

func (h *ResourceAction) waitOSSHDFSRestored(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	paused, result, err := h.getOSSHDFSSafeMode(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	data["hdfs_safe_mode"] = paused
	data["hdfs_safe_mode_read_request_id"] = result.RequestID
	if paused {
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID,
			Data:              data,
			RetryAfter:        ossDataLakeCleanupPollInterval,
		}, nil
	}
	data["phase"] = ossRetryDeleteBucketPhase
	data["hdfs_restored"] = true
	data["hdfs_safe_mode_requested"] = false
	return ossBucketCleanupResult(data, result.RequestID), nil
}

func (h *ResourceAction) deleteOSSHDFSFilesPage(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	parameters := h.ossHDFSRequestParameters(request, "getListing", map[string]any{
		"path": url.PathEscape("/"), "maxkeys": ossBucketCleanupPageSize,
		"marker": "", "needLocation": false,
	})
	listed, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossListBucketHDFSFilesOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := ossHDFSApplicationError(listed.Data); err != nil {
		return contracts.ActionResult{}, err
	}
	root, err := ossHDFSResponseRoot(listed.Data)
	if err != nil {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud OSS HDFS getListing response: %w", err,
		)
	}
	files, err := ossResponseItems(root, "fileStatuses.status")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	data["hdfs_list_request_id"] = listed.RequestID
	lastRequestID := listed.RequestID
	for _, item := range files {
		file, ok := productAPIResourceMap(item)
		if !ok {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS HDFS getListing returned a non-object status",
			)
		}
		path, err := url.PathUnescape(strings.TrimSpace(stringValue(file["path"])))
		if err != nil || path == "" {
			if err == nil {
				err = errors.New("empty path")
			}
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud OSS HDFS getListing path: %w", err,
			)
		}
		path = "/" + strings.TrimPrefix(path, "/")
		removed, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    ossDeleteBucketHDFSFileOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: h.ossHDFSRequestParameters(request, "remove", map[string]any{
				"path": url.PathEscape(path), "recursive": true,
			}),
			IdempotencyKey: request.IdempotencyKey + ":delete-hdfs:" + path,
		})
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if err := ossHDFSApplicationError(removed.Data); err != nil {
			return contracts.ActionResult{}, err
		}
		removeRoot, err := ossHDFSResponseRoot(removed.Data)
		if err != nil || !boolValue(removeRoot["result"]) {
			if err == nil {
				err = fmt.Errorf("remove returned result=%q", stringValue(removeRoot["result"]))
			}
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud OSS HDFS remove %q: %w", path, err,
			)
		}
		data["hdfs_files_deleted"] = integerMapValue(data, "hdfs_files_deleted") + 1
		lastRequestID = removed.RequestID
	}
	if len(files) > 0 || boolValue(root["isTruncated"]) {
		return ossBucketCleanupResult(data, lastRequestID), nil
	}
	data["phase"] = ossDeleteObjectVersionsPhase
	data["cleanup_pass"] = 1
	data["hdfs_namespace_empty"] = true
	return ossBucketCleanupResult(data, listed.RequestID), nil
}

func (h *ResourceAction) ossHDFSConfigParameters(
	request contracts.ActionRequest,
	requestType string,
) map[string]any {
	return h.ossHDFSRequestParameters(request, requestType, nil)
}

func (h *ResourceAction) ossHDFSRequestParameters(
	request contracts.ActionRequest,
	requestType string,
	requestParameters map[string]any,
) map[string]any {
	bucket := request.Asset.Identity.NativeID
	if requestParameters == nil {
		requestParameters = make(map[string]any)
	}
	return map[string]any{
		"bucket": bucket, "location": h.region,
		"x-oss-dfs-ns":            bucket,
		"x-oss-dfs-requester":     ossHDFSProtocolUser,
		"x-oss-dfs-source-addr":   ossHDFSProtocolUser,
		"x-oss-hdfs-extend-field": requestType,
		"request": map[string]any{
			"clientVersion": ossHDFSProtocolClientVersion,
			"instanceName":  bucket,
			"requestType":   requestType,
			"user":          ossHDFSProtocolUser,
			"parameters":    requestParameters,
		},
	}
}

func ossHDFSResponseRoot(data map[string]any) (map[string]any, error) {
	if root, ok := productAPIResourceMap(data["response"]); ok {
		return root, nil
	}
	if _, flattened := data["errCode"]; flattened {
		return data, nil
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	return nil, errors.New("missing response")
}

func ossHDFSApplicationError(data map[string]any) error {
	root, err := ossHDFSResponseRoot(data)
	if err != nil {
		return err
	}
	code := strings.TrimSpace(stringValue(root["errCode"]))
	if code == "" || code == "0" {
		return nil
	}
	message := strings.TrimSpace(stringValue(root["errMsg"]))
	if decoded, decodeErr := url.QueryUnescape(message); decodeErr == nil {
		message = decoded
	}
	return fmt.Errorf("Alibaba Cloud OSS HDFS request failed: code=%s message=%s", code, message)
}

func ossHDFSSafeModeEnabled(data map[string]any) (bool, error) {
	root, err := ossHDFSResponseRoot(data)
	if err != nil {
		return false, err
	}
	property, ok := productAPIResourceMap(valueAtPath(
		root, "NamespaceConfiguration.property",
	))
	if !ok {
		return false, errors.New("missing NamespaceConfiguration.property")
	}
	if !strings.EqualFold(
		strings.TrimSpace(stringValue(property["name"])),
		ossHDFSSafeModeConfigName,
	) {
		return false, fmt.Errorf("unexpected config name %q", stringValue(property["name"]))
	}
	value := strings.TrimSpace(stringValue(property["value"]))
	if !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
		return false, fmt.Errorf("invalid safe-mode value %q", value)
	}
	return strings.EqualFold(value, "true"), nil
}

func (h *ResourceAction) advanceOSSBucketCleanup(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	phase := strings.TrimSpace(stringValue(state["phase"]))
	switch phase {
	case ossDeleteObjectVersionsPhase:
		return h.deleteOSSObjectVersionsPage(ctx, request, state)
	case ossDeleteCurrentObjectsPhase:
		return h.deleteOSSCurrentObjectsPage(ctx, request, state)
	case ossAbortMultipartUploadsPhase:
		return h.abortOSSMultipartUploadsPage(ctx, request, state)
	case ossDeleteLiveChannelsPhase:
		return h.deleteOSSLiveChannelsPage(ctx, request, state)
	case ossRetryDeleteBucketPhase:
		return h.retryOSSBucketDelete(ctx, request, state)
	case ossPauseHDFSPhase:
		return h.pauseOSSHDFS(ctx, request, state)
	case ossWaitHDFSPausedPhase:
		return h.waitOSSHDFSPaused(ctx, request, state)
	case ossDeleteHDFSFilesPhase:
		return h.deleteOSSHDFSFilesPage(ctx, request, state)
	case ossRestoreHDFSPhase:
		return h.restoreOSSHDFS(ctx, request, state)
	case ossWaitHDFSRestoredPhase:
		return h.waitOSSHDFSRestored(ctx, request, state)
	case ossDisableDataLakeStoragePhase:
		return h.pauseOSSHDFS(ctx, request, state)
	case ossWaitDataLakeCleanupPhase:
		return h.pauseOSSHDFS(ctx, request, state)
	default:
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud OSS bucket cleanup has unsupported phase %q",
			phase,
		)
	}
}

func (h *ResourceAction) ossBucketParameters(
	request contracts.ActionRequest,
) map[string]any {
	return map[string]any{
		"bucket":   request.Asset.Identity.NativeID,
		"location": h.region,
	}
}

func (h *ResourceAction) deleteOSSObjectVersionsPage(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	parameters := h.ossBucketParameters(request)
	parameters["max-keys"] = strconv.Itoa(ossBucketCleanupPageSize)
	parameters["encoding-type"] = "url"
	if marker := strings.TrimSpace(stringValue(state["object_key_marker"])); marker != "" {
		parameters["key-marker"] = marker
	}
	if marker := strings.TrimSpace(stringValue(state["object_version_id_marker"])); marker != "" {
		parameters["version-id-marker"] = marker
	}
	listed, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossListObjectVersionsOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	root, err := ossResponseRoot(listed.Data, "ListVersionsResult")
	if err != nil {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud OSS ListObjectVersions response: %w",
			err,
		)
	}
	versions, err := ossResponseItems(root, "Version")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	deleteMarkers, err := ossResponseItems(root, "DeleteMarker")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	deleted, skippedDataLake, requestID, err := h.deleteOSSObjectEntries(
		ctx,
		request,
		root,
		append(versions, deleteMarkers...),
		boolValue(state["hdfs_namespace_empty"]),
	)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data["object_versions_deleted"] = integerMapValue(data, "object_versions_deleted") + deleted
	if skippedDataLake > 0 {
		data["data_lake_storage_detected"] = true
		data["data_lake_entries_skipped"] = integerMapValue(data, "data_lake_entries_skipped") + skippedDataLake
	}
	data["list_object_versions_request_id"] = listed.RequestID
	if requestID != "" {
		data["delete_object_request_id"] = requestID
	}
	if boolValue(root["IsTruncated"]) {
		keyMarker, err := decodedOSSResponseValue(root, "NextKeyMarker")
		if err != nil || keyMarker == "" {
			if err == nil {
				err = errors.New("truncated response omitted NextKeyMarker")
			}
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud OSS ListObjectVersions pagination: %w",
				err,
			)
		}
		versionMarker, err := decodedOSSResponseValue(root, "NextVersionIdMarker")
		if err != nil {
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud OSS ListObjectVersions version marker: %w",
				err,
			)
		}
		data["object_key_marker"] = keyMarker
		data["object_version_id_marker"] = versionMarker
		return ossBucketCleanupResult(data, listed.RequestID), nil
	}
	delete(data, "object_key_marker")
	delete(data, "object_version_id_marker")
	data["phase"] = ossDeleteCurrentObjectsPhase
	return ossBucketCleanupResult(data, listed.RequestID), nil
}

func (h *ResourceAction) deleteOSSCurrentObjectsPage(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	parameters := h.ossBucketParameters(request)
	parameters["max-keys"] = strconv.Itoa(ossBucketCleanupPageSize)
	parameters["encoding-type"] = "url"
	listed, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossListObjectsOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	root, err := ossResponseRoot(listed.Data, "ListBucketResult")
	if err != nil {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud OSS ListObjects response: %w",
			err,
		)
	}
	contents, err := ossResponseItems(root, "Contents")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	objects := make([]any, 0, len(contents))
	skippedDataLake := 0
	for _, item := range contents {
		resource, ok := productAPIResourceMap(item)
		if !ok {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS ListObjects returned a non-object entry",
			)
		}
		key, decodeErr := decodedOSSObjectKey(root, resource)
		if decodeErr != nil {
			return contracts.ActionResult{}, decodeErr
		}
		if key == "" {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS object is missing Key",
			)
		}
		if ossDataLakeInternalKey(key) && !boolValue(state["hdfs_namespace_empty"]) {
			skippedDataLake++
			continue
		}
		objects = append(objects, map[string]any{"Key": key})
	}
	data := cloneTopologyMap(state)
	lastRequestID := ""
	for start := 0; start < len(objects); start += ossBucketCleanupPageSize {
		end := min(start+ossBucketCleanupPageSize, len(objects))
		deleteParameters := h.ossBucketParameters(request)
		deleteParameters["Delete"] = map[string]any{
			"Object": objects[start:end],
			"Quiet":  true,
		}
		deleted, deleteErr := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    ossDeleteMultipleObjectsOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters:   deleteParameters,
			IdempotencyKey: fmt.Sprintf(
				"%s:delete-current-objects:%d",
				request.IdempotencyKey,
				integerMapValue(data, "current_objects_deleted")+start,
			),
		})
		if deleteErr != nil {
			return contracts.ActionResult{}, deleteErr
		}
		lastRequestID = deleted.RequestID
	}
	data["current_objects_deleted"] = integerMapValue(data, "current_objects_deleted") + len(objects)
	data["list_objects_request_id"] = listed.RequestID
	if skippedDataLake > 0 {
		data["data_lake_storage_detected"] = true
		data["data_lake_entries_skipped"] = integerMapValue(data, "data_lake_entries_skipped") + skippedDataLake
	}
	if lastRequestID != "" {
		data["delete_object_request_id"] = lastRequestID
	}
	// Always list from the start again after deletion. This avoids marker gaps
	// when the current page disappears and also verifies eventual consistency.
	if len(objects) > 0 {
		return ossBucketCleanupResult(data, lastRequestID), nil
	}
	data["phase"] = ossAbortMultipartUploadsPhase
	return ossBucketCleanupResult(data, listed.RequestID), nil
}

func (h *ResourceAction) deleteOSSObjectEntries(
	ctx context.Context,
	request contracts.ActionRequest,
	root map[string]any,
	items []any,
	deleteDataLakeInternal bool,
) (int, int, string, error) {
	objects := make([]any, 0, len(items))
	skippedDataLake := 0
	for _, item := range items {
		resource, ok := productAPIResourceMap(item)
		if !ok {
			return 0, skippedDataLake, "", errors.New(
				"Alibaba Cloud OSS ListObjectVersions returned a non-object entry",
			)
		}
		key, err := decodedOSSObjectKey(root, resource)
		if err != nil {
			return 0, skippedDataLake, "", err
		}
		versionID := strings.TrimSpace(stringValue(resource["VersionId"]))
		if key == "" || versionID == "" {
			return 0, skippedDataLake, "", errors.New(
				"Alibaba Cloud OSS object version is missing Key or VersionId",
			)
		}
		if ossDataLakeInternalKey(key) && !deleteDataLakeInternal {
			skippedDataLake++
			continue
		}
		objects = append(objects, map[string]any{
			"Key": key, "VersionId": versionID,
		})
	}
	if len(objects) == 0 {
		return 0, skippedDataLake, "", nil
	}
	lastRequestID := ""
	for start := 0; start < len(objects); start += ossBucketCleanupPageSize {
		end := min(start+ossBucketCleanupPageSize, len(objects))
		parameters := h.ossBucketParameters(request)
		parameters["Delete"] = map[string]any{
			"Object": objects[start:end],
			"Quiet":  true,
		}
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    ossDeleteMultipleObjectsOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters:   parameters,
			IdempotencyKey: fmt.Sprintf(
				"%s:delete-object-versions:%d",
				request.IdempotencyKey,
				start/ossBucketCleanupPageSize,
			),
		})
		if err != nil {
			return 0, skippedDataLake, "", err
		}
		lastRequestID = result.RequestID
	}
	return len(objects), skippedDataLake, lastRequestID, nil
}

func ossDataLakeInternalKey(key string) bool {
	return key == ".dlsdata" || strings.HasPrefix(key, ".dlsdata/")
}

func (h *ResourceAction) abortOSSMultipartUploadsPage(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	parameters := h.ossBucketParameters(request)
	parameters["max-uploads"] = strconv.Itoa(ossBucketCleanupPageSize)
	parameters["encoding-type"] = "url"
	if marker := strings.TrimSpace(stringValue(state["upload_key_marker"])); marker != "" {
		parameters["key-marker"] = marker
	}
	if marker := strings.TrimSpace(stringValue(state["upload_id_marker"])); marker != "" {
		parameters["upload-id-marker"] = marker
	}
	listed, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossListMultipartUploadsOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	root, err := ossResponseRoot(listed.Data, "ListMultipartUploadsResult")
	if err != nil {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud OSS ListMultipartUploads response: %w",
			err,
		)
	}
	uploads, err := ossResponseItems(root, "Upload")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	lastRequestID := ""
	for _, item := range uploads {
		upload, ok := productAPIResourceMap(item)
		if !ok {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS ListMultipartUploads returned a non-object entry",
			)
		}
		key, err := decodedOSSObjectKey(root, upload)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		uploadID := strings.TrimSpace(stringValue(upload["UploadId"]))
		if key == "" || uploadID == "" {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS multipart upload is missing Key or UploadId",
			)
		}
		abortParameters := h.ossBucketParameters(request)
		abortParameters["object"] = key
		abortParameters["uploadId"] = uploadID
		aborted, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    ossAbortMultipartUploadOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters:   abortParameters,
			IdempotencyKey: request.IdempotencyKey +
				":abort-multipart-upload",
		})
		if err != nil {
			return contracts.ActionResult{}, err
		}
		data["multipart_uploads_aborted"] = integerMapValue(data, "multipart_uploads_aborted") + 1
		lastRequestID = aborted.RequestID
	}
	data["list_multipart_uploads_request_id"] = listed.RequestID
	if lastRequestID != "" {
		data["abort_multipart_upload_request_id"] = lastRequestID
	}
	if boolValue(root["IsTruncated"]) {
		keyMarker, err := decodedOSSResponseValue(root, "NextKeyMarker")
		if err != nil || keyMarker == "" {
			if err == nil {
				err = errors.New("truncated response omitted NextKeyMarker")
			}
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud OSS ListMultipartUploads pagination: %w",
				err,
			)
		}
		uploadMarker := strings.TrimSpace(stringValue(root["NextUploadIdMarker"]))
		data["upload_key_marker"] = keyMarker
		data["upload_id_marker"] = uploadMarker
		return ossBucketCleanupResult(data, listed.RequestID), nil
	}
	delete(data, "upload_key_marker")
	delete(data, "upload_id_marker")
	data["phase"] = ossDeleteLiveChannelsPhase
	return ossBucketCleanupResult(data, listed.RequestID), nil
}

func (h *ResourceAction) deleteOSSLiveChannelsPage(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	parameters := h.ossBucketParameters(request)
	parameters["max-keys"] = strconv.Itoa(ossBucketCleanupPageSize)
	if marker := strings.TrimSpace(stringValue(state["live_channel_marker"])); marker != "" {
		parameters["marker"] = marker
	}
	listed, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ossListLiveChannelOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	root, err := ossResponseRoot(listed.Data, "ListLiveChannelResult")
	if err != nil {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud OSS ListLiveChannel response: %w",
			err,
		)
	}
	channels, err := ossResponseItems(root, "LiveChannel")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	lastRequestID := ""
	for _, item := range channels {
		channel, ok := productAPIResourceMap(item)
		if !ok {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS ListLiveChannel returned a non-object entry",
			)
		}
		name := strings.TrimSpace(stringValue(channel["Name"]))
		if name == "" {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS LiveChannel is missing Name",
			)
		}
		deleteParameters := h.ossBucketParameters(request)
		deleteParameters["channel"] = name
		deleted, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    ossDeleteLiveChannelOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters:   deleteParameters,
			IdempotencyKey: request.IdempotencyKey +
				":delete-live-channel",
		})
		if err != nil {
			return contracts.ActionResult{}, err
		}
		data["live_channels_deleted"] = integerMapValue(data, "live_channels_deleted") + 1
		lastRequestID = deleted.RequestID
	}
	data["list_live_channels_request_id"] = listed.RequestID
	if lastRequestID != "" {
		data["delete_live_channel_request_id"] = lastRequestID
	}
	if boolValue(root["IsTruncated"]) {
		marker := strings.TrimSpace(stringValue(root["NextMarker"]))
		if marker == "" {
			return contracts.ActionResult{}, errors.New(
				"Alibaba Cloud OSS ListLiveChannel truncated response omitted NextMarker",
			)
		}
		data["live_channel_marker"] = marker
		return ossBucketCleanupResult(data, listed.RequestID), nil
	}
	delete(data, "live_channel_marker")
	data["phase"] = ossRetryDeleteBucketPhase
	return ossBucketCleanupResult(data, listed.RequestID), nil
}

func (h *ResourceAction) retryOSSBucketDelete(
	ctx context.Context,
	request contracts.ActionRequest,
	state map[string]any,
) (contracts.ActionResult, error) {
	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if boolValue(state["data_lake_storage_detected"]) ||
		boolValue(state["hdfs_cleanup_automatic"]) {
		operation = ossDeleteDataLakeBucketOperation
		parameters = h.ossBucketParameters(request)
	}
	deleted, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    operation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
		IdempotencyKey: request.IdempotencyKey +
			":delete-empty-bucket",
	})
	if err != nil {
		if providerError, dataLake := ossDataLakeCleanupTrigger(err); dataLake {
			return h.startOSSDataLakeStorageDisable(state, providerError), nil
		}
		providerError, cleanup := ossBucketCleanupTrigger(err)
		pass := integerMapValue(state, "cleanup_pass")
		if cleanup && pass < ossBucketCleanupMaxPass {
			data := cloneTopologyMap(state)
			data["phase"] = ossDeleteObjectVersionsPhase
			if strings.TrimSpace(stringValue(state["phase"])) == ossWaitDataLakeCleanupPhase {
				data["cleanup_pass"] = 1
				data["data_lake_cleanup_completed"] = true
				delete(data, "manual_action_required")
				delete(data, "data_lake_cleanup_steps")
				delete(data, "data_lake_cleanup_command")
			} else {
				data["cleanup_pass"] = pass + 1
			}
			data["trigger_code"] = providerError.Code
			data["trigger_message"] = providerError.Message
			return ossBucketCleanupResult(data, providerError.RequestID), nil
		}
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(state)
	data["phase"] = ossBucketDeleteRequestedPhase
	data["delete_bucket_request_id"] = deleted.RequestID
	return contracts.ActionResult{
		ProviderRequestID: deleted.RequestID,
		Data:              data,
	}, nil
}

func ossBucketCleanupResult(
	data map[string]any,
	requestID string,
) contracts.ActionResult {
	return contracts.ActionResult{
		ProviderRequestID: requestID,
		Data:              data,
		RetryAfter:        ossBucketCleanupPollInterval,
	}
}

func ossResponseRoot(data map[string]any, path string) (map[string]any, error) {
	if root, ok := productAPIResourceMap(valueAtPath(data, path)); ok {
		return root, nil
	}
	if _, flattened := data["IsTruncated"]; flattened {
		return data, nil
	}
	return nil, fmt.Errorf("missing %s", path)
}

func ossResponseItems(root map[string]any, name string) ([]any, error) {
	value := valueAtPath(root, name)
	if value == nil {
		return nil, nil
	}
	if items, ok := productAPIListValue(value); ok {
		return items, nil
	}
	if _, ok := productAPIResourceMap(value); ok {
		return []any{value}, nil
	}
	return nil, fmt.Errorf("Alibaba Cloud OSS response field %s is not a resource collection", name)
}

func decodedOSSObjectKey(root, resource map[string]any) (string, error) {
	key := strings.TrimSpace(stringValue(resource["Key"]))
	if !strings.EqualFold(strings.TrimSpace(stringValue(root["EncodingType"])), "url") {
		return key, nil
	}
	decoded, err := url.PathUnescape(key)
	if err != nil {
		return "", fmt.Errorf("decode Alibaba Cloud OSS object key: %w", err)
	}
	return decoded, nil
}

func decodedOSSResponseValue(root map[string]any, name string) (string, error) {
	value := strings.TrimSpace(stringValue(root[name]))
	if value == "" || !strings.EqualFold(strings.TrimSpace(stringValue(root["EncodingType"])), "url") {
		return value, nil
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", fmt.Errorf("decode Alibaba Cloud OSS %s: %w", name, err)
	}
	return decoded, nil
}

func integerMapValue(data map[string]any, name string) int {
	value, _ := integerValue(data[name])
	return value
}

type imageVisibilityPreparation struct {
	exists    bool
	changed   bool
	requestID string
}

type imageSharePreparation struct {
	exists        bool
	changed       bool
	requestID     string
	readRequestID string
	removedCount  int
	remaining     int
}

func (h *ResourceAction) makeImagePrivateIfPublic(
	ctx context.Context,
	request contracts.ActionRequest,
) (imageVisibilityPreparation, error) {
	if h.nativeType != ECSImageNativeType {
		return imageVisibilityPreparation{exists: true}, nil
	}
	readback, _, resource, err := h.readbackDetails(ctx, request)
	if err != nil {
		return imageVisibilityPreparation{}, err
	}
	if !readback.Exists {
		return imageVisibilityPreparation{}, nil
	}
	preparation := imageVisibilityPreparation{exists: true}
	if !boolValue(resource["IsPublic"]) {
		return preparation, nil
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ecsModifyImageSharePermissionOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"RegionId": h.region,
			"ImageId":  request.Asset.Identity.NativeID,
			"IsPublic": false,
		},
		IdempotencyKey: request.IdempotencyKey + ":make-private",
	})
	if err != nil {
		var providerCall *contracts.ProviderCallError
		if !errors.As(err, &providerCall) ||
			strings.TrimSpace(providerCall.Provider.Code) != "Image.NotPublic" {
			return imageVisibilityPreparation{}, err
		}
		// Another actor (or a prior request whose result was not persisted)
		// already completed the desired visibility change. Continue through
		// readback instead of turning the idempotent state into a failure.
		preparation.changed = true
		preparation.requestID = providerCall.Provider.RequestID
		return preparation, nil
	}
	preparation.changed = true
	preparation.requestID = result.RequestID
	return preparation, nil
}

func (h *ResourceAction) removeImageShareAccounts(
	ctx context.Context,
	request contracts.ActionRequest,
) (imageSharePreparation, error) {
	if h.nativeType != ECSImageNativeType {
		return imageSharePreparation{exists: true}, nil
	}
	accounts, readRequestID, exists, err := h.listImageShareAccounts(ctx, request)
	if err != nil {
		return imageSharePreparation{}, err
	}
	preparation := imageSharePreparation{
		exists: exists, readRequestID: readRequestID,
	}
	if !exists || len(accounts) == 0 {
		return preparation, nil
	}

	// ModifyImageSharePermission accepts at most 10 accounts per request.
	const maxAccountsPerRequest = 10
	batchSize := len(accounts)
	if batchSize > maxAccountsPerRequest {
		batchSize = maxAccountsPerRequest
	}
	parameters := map[string]any{
		"RegionId": h.region,
		"ImageId":  request.Asset.Identity.NativeID,
	}
	for index, accountID := range accounts[:batchSize] {
		parameters["RemoveAccount."+strconv.Itoa(index+1)] = accountID
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    ecsModifyImageSharePermissionOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
		IdempotencyKey: request.IdempotencyKey +
			":remove-image-share-accounts",
	})
	if isImageNotFound(err) {
		return imageSharePreparation{}, nil
	}
	if err != nil {
		return imageSharePreparation{}, err
	}
	preparation.changed = true
	preparation.requestID = result.RequestID
	preparation.removedCount = batchSize
	preparation.remaining = len(accounts) - batchSize
	return preparation, nil
}

func (h *ResourceAction) listImageShareAccounts(
	ctx context.Context,
	request contracts.ActionRequest,
) ([]string, string, bool, error) {
	const pageSize = 100
	accounts := make([]string, 0)
	lastRequestID := ""
	for page := 1; ; page++ {
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    ecsDescribeImageSharePermissionOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId":   h.region,
				"ImageId":    request.Asset.Identity.NativeID,
				"PageNumber": page,
				"PageSize":   pageSize,
			},
		})
		if isNotFound(err) {
			return nil, result.RequestID, false, nil
		}
		if err != nil {
			return nil, "", false, err
		}
		lastRequestID = result.RequestID
		returnedImageID := strings.TrimSpace(stringValue(result.Data["ImageId"]))
		if returnedImageID != "" && returnedImageID != request.Asset.Identity.NativeID {
			return nil, "", false, fmt.Errorf(
				"Alibaba Cloud DescribeImageSharePermission returned image %q, want %q",
				returnedImageID, request.Asset.Identity.NativeID,
			)
		}
		items, ok := productAPIListValue(valueAtPath(result.Data, "Accounts.Account"))
		if !ok {
			if total, totalOK := integerValue(result.Data["TotalCount"]); totalOK && total == 0 {
				items = []any{}
			} else {
				return nil, "", false, fmt.Errorf(
					"Alibaba Cloud DescribeImageSharePermission returned an invalid Accounts.Account collection",
				)
			}
		}
		for _, item := range items {
			account, ok := productAPIResourceMap(item)
			if !ok {
				return nil, "", false, fmt.Errorf(
					"Alibaba Cloud DescribeImageSharePermission returned a non-object account",
				)
			}
			accountID := strings.TrimSpace(stringValue(account["AliyunId"]))
			if accountID == "" {
				return nil, "", false, fmt.Errorf(
					"Alibaba Cloud DescribeImageSharePermission returned an account without AliyunId",
				)
			}
			accounts = append(accounts, accountID)
		}
		total, hasTotal := integerValue(result.Data["TotalCount"])
		if len(items) == 0 || hasTotal && page*pageSize >= total || !hasTotal && len(items) < pageSize {
			break
		}
	}
	sort.Strings(accounts)
	accounts = deduplicateSortedStrings(accounts)
	return accounts, lastRequestID, true, nil
}

func deduplicateSortedStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func imageSharePermissionChangeResult(
	preparation imageSharePreparation,
	retryAfter time.Duration,
) contracts.ActionResult {
	return contracts.ActionResult{
		ProviderRequestID: preparation.requestID,
		Data: map[string]any{
			"phase":                                imageSharePermissionChangePhase,
			"image_share_accounts_removed":         preparation.removedCount,
			"image_share_accounts_remaining":       preparation.remaining,
			"share_permission_request_id":          preparation.requestID,
			"share_permission_readback_request_id": preparation.readRequestID,
		},
		RetryAfter: retryAfter,
	}
}

func isImageNotFound(err error) bool {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) {
		return false
	}
	code := strings.TrimSpace(providerCall.Provider.Code)
	return strings.EqualFold(code, "InvalidImageId.NotFound") ||
		strings.EqualFold(code, "InvalidImage.NotFound")
}

type rosStackInstance struct {
	accountID string
	regionID  string
}

func (h *ResourceAction) deleteROSStackGroup(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	readback, result, group, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !readback.Exists {
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID,
			Data:              map[string]any{"phase": "absent"},
			RetryAfter:        h.pollInterval(),
		}, nil
	}
	if deletionInProgressState(readback.State) {
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID,
			Data: map[string]any{
				"phase": rosDeleteStackGroupRequestedPhase,
				"state": readback.State,
			},
			RetryAfter: h.pollInterval(),
		}, nil
	}
	return h.advanceROSStackGroupDeletionWithGroup(ctx, request, group)
}

func (h *ResourceAction) advanceROSStackGroupDeletion(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	readback, result, group, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !readback.Exists {
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID,
			Data:              map[string]any{"phase": "absent"},
			RetryAfter:        h.pollInterval(),
		}, nil
	}
	if deletionInProgressState(readback.State) {
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID,
			Data: map[string]any{
				"phase": rosDeleteStackGroupRequestedPhase,
				"state": readback.State,
			},
			RetryAfter: h.pollInterval(),
		}, nil
	}
	return h.advanceROSStackGroupDeletionWithGroup(ctx, request, group)
}

func (h *ResourceAction) advanceROSStackGroupDeletionWithGroup(
	ctx context.Context,
	request contracts.ActionRequest,
	group map[string]any,
) (contracts.ActionResult, error) {
	groupName := strings.TrimSpace(request.Asset.Identity.NativeID)
	instances, listRequestID, err := h.listROSStackInstances(ctx, groupName)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if len(instances) > 0 {
		instance := instances[0]
		regionIDs, marshalErr := json.Marshal([]string{instance.regionID})
		if marshalErr != nil {
			return contracts.ActionResult{}, fmt.Errorf("encode ROS stack instance regions: %w", marshalErr)
		}
		parameters := map[string]any{
			"RegionId": h.region, "StackGroupName": groupName,
			"RegionIds": string(regionIDs), "RetainStacks": false,
		}
		permissionModel := strings.TrimSpace(stringValue(group["PermissionModel"]))
		if permissionModel == "" {
			permissionModel = normalizedActionValue(request.Asset.Normalized, "PermissionModel")
		}
		switch strings.ToUpper(permissionModel) {
		case "SERVICE_MANAGED":
			deploymentTargets, marshalErr := json.Marshal(map[string]any{
				"AccountIds": []string{instance.accountID},
			})
			if marshalErr != nil {
				return contracts.ActionResult{}, fmt.Errorf("encode ROS deployment targets: %w", marshalErr)
			}
			parameters["DeploymentTargets"] = string(deploymentTargets)
		case "SELF_MANAGED":
			accountIDs, marshalErr := json.Marshal([]string{instance.accountID})
			if marshalErr != nil {
				return contracts.ActionResult{}, fmt.Errorf("encode ROS stack instance accounts: %w", marshalErr)
			}
			parameters["AccountIds"] = string(accountIDs)
		default:
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud ROS stack group %q has unsupported permission model %q",
				groupName,
				permissionModel,
			)
		}
		result, invokeErr := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    rosDeleteStackInstancesOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters:   parameters,
			IdempotencyKey: request.IdempotencyKey + ":delete-stack-instance:" +
				instance.accountID + ":" + instance.regionID,
		})
		if invokeErr != nil {
			return contracts.ActionResult{}, invokeErr
		}
		operationID := strings.TrimSpace(stringValue(result.Data["OperationId"]))
		if operationID == "" {
			operationID = strings.TrimSpace(result.OperationID)
		}
		if operationID == "" {
			return contracts.ActionResult{}, fmt.Errorf(
				"Alibaba Cloud ROS DeleteStackInstances for stack group %q returned no OperationId",
				groupName,
			)
		}
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID, ProviderOperationID: operationID,
			Data: map[string]any{
				"phase": rosDeleteStackInstancesPhase, "operation_id": operationID,
				"account_id": instance.accountID, "region_id": instance.regionID,
				"permission_model":               permissionModel,
				"stack_instance_read_request_id": listRequestID,
			},
			RetryAfter: h.pollInterval(),
		}, nil
	}

	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	deleted, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID, Operation: operation,
		Scope: map[string]string{"region": h.region}, Parameters: parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(deleted.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = rosDeleteStackGroupRequestedPhase
	data["stack_instance_read_request_id"] = listRequestID
	return contracts.ActionResult{
		ProviderRequestID: deleted.RequestID, ProviderOperationID: deleted.OperationID,
		Data: data, RetryAfter: h.pollInterval(),
	}, nil
}

func (h *ResourceAction) listROSStackInstances(
	ctx context.Context,
	stackGroupName string,
) ([]rosStackInstance, string, error) {
	const pageSize = 50
	instances := make([]rosStackInstance, 0)
	requestID := ""
	for page := 1; page <= 1000; page++ {
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    rosListStackInstancesOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId": h.region, "StackGroupName": stackGroupName,
				"PageNumber": page, "PageSize": pageSize,
			},
		})
		if isNotFound(err) {
			return nil, result.RequestID, nil
		}
		if err != nil {
			return nil, requestID, err
		}
		requestID = result.RequestID
		items, ok := productAPIListValue(result.Data["StackInstances"])
		if !ok && result.Data["StackInstances"] != nil {
			return nil, requestID, fmt.Errorf(
				"Alibaba Cloud ROS ListStackInstances returned an invalid StackInstances collection",
			)
		}
		for _, item := range items {
			instance, itemOK := productAPIResourceMap(item)
			if !itemOK {
				return nil, requestID, fmt.Errorf(
					"Alibaba Cloud ROS ListStackInstances returned a non-object stack instance",
				)
			}
			returnedGroupName := strings.TrimSpace(stringValue(instance["StackGroupName"]))
			if returnedGroupName != "" && returnedGroupName != stackGroupName {
				return nil, requestID, fmt.Errorf(
					"Alibaba Cloud ROS ListStackInstances returned stack group %q, want %q",
					returnedGroupName,
					stackGroupName,
				)
			}
			accountID := strings.TrimSpace(stringValue(instance["AccountId"]))
			regionID := strings.TrimSpace(stringValue(instance["RegionId"]))
			if accountID == "" || regionID == "" {
				return nil, requestID, fmt.Errorf(
					"Alibaba Cloud ROS stack instance in group %q requires AccountId and RegionId",
					stackGroupName,
				)
			}
			instances = append(instances, rosStackInstance{accountID: accountID, regionID: regionID})
		}
		total, hasTotal := integerValue(result.Data["TotalCount"])
		if len(items) == 0 || hasTotal && page*pageSize >= total || !hasTotal && len(items) < pageSize {
			break
		}
		if page == 1000 {
			return nil, requestID, fmt.Errorf(
				"Alibaba Cloud ROS ListStackInstances exceeded pagination limit for stack group %q",
				stackGroupName,
			)
		}
	}
	sort.Slice(instances, func(i, j int) bool {
		left := instances[i].accountID + "\x00" + instances[i].regionID
		right := instances[j].accountID + "\x00" + instances[j].regionID
		return left < right
	})
	return instances, requestID, nil
}

func (h *ResourceAction) deletePrivateLinkEndpointService(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	readback, result, _, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !readback.Exists {
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID,
			Data:              map[string]any{"phase": "absent"},
			RetryAfter:        h.pollInterval(),
		}, nil
	}
	return h.advancePrivateLinkEndpointServiceDeletion(ctx, request)
}

func (h *ResourceAction) advancePrivateLinkEndpointServiceDeletion(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	serviceID := request.Asset.Identity.NativeID
	connections, requestID, err := h.listPrivateLinkEndpointConnections(ctx, serviceID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	sort.Slice(connections, func(i, j int) bool {
		return stringValue(connections[i]["EndpointId"]) < stringValue(connections[j]["EndpointId"])
	})
	for _, connection := range connections {
		endpointID := strings.TrimSpace(stringValue(connection["EndpointId"]))
		status := strings.TrimSpace(stringValue(connection["ConnectionStatus"]))
		if endpointID == "" || strings.EqualFold(status, "Disconnected") ||
			strings.EqualFold(status, "ServiceDeleted") {
			continue
		}
		data := map[string]any{
			"phase": "disconnect_endpoint_connections", "endpoint_id": endpointID,
			"connection_status": status,
		}
		if strings.EqualFold(status, "Disconnecting") ||
			strings.EqualFold(status, "Deleting") ||
			strings.EqualFold(status, "Pending") ||
			strings.EqualFold(status, "Connecting") {
			return contracts.ActionResult{
				ProviderRequestID: requestID, Data: data, RetryAfter: h.pollInterval(),
			}, nil
		}
		result, invokeErr := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    privateLinkDisableConnectionOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId": h.region, "ServiceId": serviceID,
				"EndpointId": endpointID, "DryRun": false,
			},
			IdempotencyKey: request.IdempotencyKey + ":disconnect:" + endpointID,
		})
		if invokeErr != nil {
			return contracts.ActionResult{}, invokeErr
		}
		data["connection_status"] = "Disconnecting"
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
			Data: data, RetryAfter: h.pollInterval(),
		}, nil
	}

	resources, resourceRequestID, err := h.listPrivateLinkEndpointServiceResources(ctx, serviceID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	sort.Slice(resources, func(i, j int) bool {
		left := stringValue(resources[i]["ResourceId"]) + "\x00" + stringValue(resources[i]["ZoneId"])
		right := stringValue(resources[j]["ResourceId"]) + "\x00" + stringValue(resources[j]["ZoneId"])
		return left < right
	})
	if len(resources) > 0 {
		resource := resources[0]
		resourceID := strings.TrimSpace(stringValue(resource["ResourceId"]))
		if resourceID == "" {
			return contracts.ActionResult{}, fmt.Errorf("Alibaba Cloud PrivateLink service resource has no ResourceId")
		}
		parameters := map[string]any{
			"RegionId": h.region, "ServiceId": serviceID,
			"ResourceId": resourceID, "DryRun": false,
		}
		if resourceType := strings.TrimSpace(stringValue(resource["ResourceType"])); resourceType != "" {
			parameters["ResourceType"] = resourceType
		}
		if zoneID := strings.TrimSpace(stringValue(resource["ZoneId"])); zoneID != "" {
			parameters["ZoneId"] = zoneID
		}
		result, invokeErr := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    privateLinkDetachResourceOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters:   parameters,
			IdempotencyKey: request.IdempotencyKey + ":detach-resource:" +
				resourceID + ":" + strings.TrimSpace(stringValue(resource["ZoneId"])),
		})
		if invokeErr != nil {
			return contracts.ActionResult{}, invokeErr
		}
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
			Data: map[string]any{
				"phase": "detach_service_resources", "resource_id": resourceID,
			},
			RetryAfter: h.pollInterval(),
		}, nil
	}

	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	deleted, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID, Operation: operation,
		Scope: map[string]string{"region": h.region}, Parameters: parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(deleted.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = "endpoint_service_delete_requested"
	if resourceRequestID != "" {
		data["resource_read_request_id"] = resourceRequestID
	}
	return contracts.ActionResult{
		ProviderRequestID: deleted.RequestID, ProviderOperationID: deleted.OperationID,
		Data: data, RetryAfter: h.pollInterval(),
	}, nil
}

func (h *ResourceAction) listPrivateLinkEndpointConnections(
	ctx context.Context,
	serviceID string,
) ([]map[string]any, string, error) {
	return h.listPrivateLinkRecords(
		ctx, privateLinkListConnectionsOperation, "Connections", serviceID, 1000,
	)
}

func (h *ResourceAction) listPrivateLinkEndpointServices(
	ctx context.Context,
) ([]map[string]any, string, error) {
	return h.listPrivateLinkRecords(
		ctx, privateLinkListServicesOperation, "Services", "", 100,
	)
}

func (h *ResourceAction) listPrivateLinkEndpointServiceResources(
	ctx context.Context,
	serviceID string,
) ([]map[string]any, string, error) {
	return h.listPrivateLinkRecords(
		ctx, privateLinkListResourcesOperation, "Resources", serviceID, 50,
	)
}

func (h *ResourceAction) listPrivateLinkRecords(
	ctx context.Context,
	operation string,
	itemsPath string,
	serviceID string,
	maxResults int,
) ([]map[string]any, string, error) {
	records := make([]map[string]any, 0)
	nextToken := ""
	requestID := ""
	for page := 0; page < 1000; page++ {
		parameters := map[string]any{
			"RegionId": h.region, "MaxResults": maxResults,
		}
		if serviceID != "" {
			parameters["ServiceId"] = serviceID
		}
		if nextToken != "" {
			parameters["NextToken"] = nextToken
		}
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID, Operation: operation,
			Scope: map[string]string{"region": h.region}, Parameters: parameters,
		})
		if err != nil {
			return nil, requestID, err
		}
		if requestID == "" {
			requestID = result.RequestID
		}
		items, ok := productAPIListValue(result.Data[itemsPath])
		if !ok && result.Data[itemsPath] != nil {
			return nil, requestID, fmt.Errorf(
				"Alibaba Cloud PrivateLink %s returned an invalid %s collection",
				operation, itemsPath,
			)
		}
		for _, item := range items {
			record, recordOK := productAPIResourceMap(item)
			if !recordOK {
				return nil, requestID, fmt.Errorf(
					"Alibaba Cloud PrivateLink %s returned a non-object %s item",
					operation, itemsPath,
				)
			}
			records = append(records, record)
		}
		nextToken = strings.TrimSpace(stringValue(result.Data["NextToken"]))
		if nextToken == "" {
			return records, requestID, nil
		}
	}
	return nil, requestID, fmt.Errorf("Alibaba Cloud PrivateLink %s exceeded pagination limit", operation)
}

type privateLinkServiceResourceBinding struct {
	serviceID    string
	resourceID   string
	resourceType string
	zoneID       string
}

type privateLinkEndpointZoneBinding struct {
	endpointID       string
	zoneID           string
	status           string
	replacedResource bool
}

func (h *ResourceAction) advancePrivateLinkServiceResourceCleanup(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, bool, error) {
	resourceType, _ := privateLinkServiceResourceType(h.nativeType)
	bindings, listRequestID, err := h.findPrivateLinkServiceResourceBindings(
		ctx,
		request.Asset.Identity.NativeID,
		resourceType,
	)
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	if len(bindings) == 0 {
		return contracts.ActionResult{}, false, nil
	}

	binding := bindings[0]
	zones, connectionRequestID, err := h.findPrivateLinkEndpointZoneBindings(
		ctx,
		binding.serviceID,
		binding.resourceID,
		binding.zoneID,
	)
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	for _, zone := range zones {
		data := map[string]any{
			"phase":       privateLinkDisconnectBoundZonePhase,
			"service_id":  binding.serviceID,
			"endpoint_id": zone.endpointID,
			"zone_id":     zone.zoneID,
			"zone_status": zone.status,
		}
		if listRequestID != "" {
			data["service_read_request_id"] = listRequestID
		}
		if connectionRequestID != "" {
			data["connection_read_request_id"] = connectionRequestID
		}
		if zone.replacedResource {
			data["replaced_resource"] = true
		}
		switch strings.ToLower(strings.TrimSpace(zone.status)) {
		case "disconnected", "servicedeleted":
			continue
		case "disconnecting", "deleting", "pending", "connecting", "migrating", "wait":
			return contracts.ActionResult{
				ProviderRequestID: connectionRequestID,
				Data:              data,
				RetryAfter:        h.pollInterval(),
			}, true, nil
		case "connected", "migrated":
			parameters := map[string]any{
				"RegionId": h.region, "ServiceId": binding.serviceID,
				"EndpointId": zone.endpointID, "ZoneId": zone.zoneID,
				"ReplacedResource": zone.replacedResource,
			}
			result, invokeErr := h.provider.Invoke(ctx, contracts.Invocation{
				ConnectionID: h.connectionID,
				Operation:    privateLinkDisableZoneConnectionOperation,
				Scope:        map[string]string{"region": h.region},
				Parameters:   parameters,
				IdempotencyKey: request.IdempotencyKey + ":disconnect-endpoint-zone:" +
					binding.serviceID + ":" + zone.endpointID + ":" + zone.zoneID,
			})
			if invokeErr != nil {
				return contracts.ActionResult{}, false, invokeErr
			}
			data["zone_status"] = "Disconnecting"
			return contracts.ActionResult{
				ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
				Data: data, RetryAfter: h.pollInterval(),
			}, true, nil
		default:
			return contracts.ActionResult{}, false, fmt.Errorf(
				"Alibaba Cloud PrivateLink endpoint %q zone %q has unsupported status %q while unbinding service resource %q",
				zone.endpointID,
				zone.zoneID,
				zone.status,
				binding.resourceID,
			)
		}
	}

	parameters := map[string]any{
		"RegionId": h.region, "ServiceId": binding.serviceID,
		"ResourceId": binding.resourceID, "DryRun": false,
	}
	if binding.resourceType != "" {
		parameters["ResourceType"] = binding.resourceType
	}
	if binding.zoneID != "" {
		parameters["ZoneId"] = binding.zoneID
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    privateLinkDetachResourceOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
		IdempotencyKey: request.IdempotencyKey + ":detach-service-resource:" +
			binding.serviceID + ":" + binding.resourceID + ":" + binding.zoneID,
	})
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: map[string]any{
			"phase": privateLinkDetachBoundResourcePhase, "service_id": binding.serviceID,
			"resource_id": binding.resourceID, "zone_id": binding.zoneID,
		},
		RetryAfter: h.pollInterval(),
	}, true, nil
}

func privateLinkServiceResourceType(nativeType string) (string, bool) {
	switch nativeType {
	case natGatewayNativeType:
		return "vpcNat", true
	case slbLoadBalancerNativeType:
		return "slb", true
	case albLoadBalancerNativeType:
		return "alb", true
	case nlbLoadBalancerNativeType:
		return "nlb", true
	case gwlbLoadBalancerNativeType:
		return "gwlb", true
	default:
		return "", false
	}
}

func (h *ResourceAction) findPrivateLinkServiceResourceBindings(
	ctx context.Context,
	resourceID string,
	defaultResourceType string,
) ([]privateLinkServiceResourceBinding, string, error) {
	services, requestID, err := h.listPrivateLinkEndpointServices(ctx)
	if err != nil {
		return nil, requestID, err
	}
	sort.Slice(services, func(i, j int) bool {
		return stringValue(services[i]["ServiceId"]) < stringValue(services[j]["ServiceId"])
	})
	bindings := make([]privateLinkServiceResourceBinding, 0)
	for _, service := range services {
		serviceID := strings.TrimSpace(stringValue(service["ServiceId"]))
		if serviceID == "" {
			return nil, requestID, fmt.Errorf("Alibaba Cloud PrivateLink endpoint service has no ServiceId")
		}
		resources, _, listErr := h.listPrivateLinkEndpointServiceResources(ctx, serviceID)
		if listErr != nil {
			return nil, requestID, listErr
		}
		for _, resource := range resources {
			if strings.TrimSpace(stringValue(resource["ResourceId"])) != resourceID {
				continue
			}
			resourceType := strings.TrimSpace(stringValue(resource["ResourceType"]))
			if resourceType == "" {
				resourceType = defaultResourceType
			}
			bindings = append(bindings, privateLinkServiceResourceBinding{
				serviceID: serviceID, resourceID: resourceID,
				resourceType: resourceType,
				zoneID:       strings.TrimSpace(stringValue(resource["ZoneId"])),
			})
		}
	}
	sort.Slice(bindings, func(i, j int) bool {
		left := bindings[i].serviceID + "\x00" + bindings[i].zoneID
		right := bindings[j].serviceID + "\x00" + bindings[j].zoneID
		return left < right
	})
	return bindings, requestID, nil
}

func (h *ResourceAction) findPrivateLinkEndpointZoneBindings(
	ctx context.Context,
	serviceID string,
	resourceID string,
	resourceZoneID string,
) ([]privateLinkEndpointZoneBinding, string, error) {
	connections, requestID, err := h.listPrivateLinkEndpointConnections(ctx, serviceID)
	if err != nil {
		return nil, requestID, err
	}
	zones := make([]privateLinkEndpointZoneBinding, 0)
	for _, connection := range connections {
		endpointID := strings.TrimSpace(stringValue(connection["EndpointId"]))
		if endpointID == "" {
			return nil, requestID, fmt.Errorf("Alibaba Cloud PrivateLink endpoint connection has no EndpointId")
		}
		rawZones, ok := productAPIListValue(connection["Zones"])
		if !ok && connection["Zones"] != nil {
			return nil, requestID, fmt.Errorf(
				"Alibaba Cloud PrivateLink endpoint %q returned an invalid Zones collection",
				endpointID,
			)
		}
		for _, rawZone := range rawZones {
			zone, zoneOK := productAPIResourceMap(rawZone)
			if !zoneOK {
				return nil, requestID, fmt.Errorf(
					"Alibaba Cloud PrivateLink endpoint %q returned a non-object zone",
					endpointID,
				)
			}
			zoneID := strings.TrimSpace(stringValue(zone["ZoneId"]))
			if resourceZoneID != "" && zoneID != resourceZoneID {
				continue
			}
			replaced := false
			switch {
			case strings.TrimSpace(stringValue(zone["ResourceId"])) == resourceID:
			case strings.TrimSpace(stringValue(zone["ReplacedResourceId"])) == resourceID:
				replaced = true
			default:
				continue
			}
			if zoneID == "" {
				return nil, requestID, fmt.Errorf(
					"Alibaba Cloud PrivateLink endpoint %q resource %q connection has no ZoneId",
					endpointID,
					resourceID,
				)
			}
			zones = append(zones, privateLinkEndpointZoneBinding{
				endpointID: endpointID, zoneID: zoneID,
				status:           strings.TrimSpace(stringValue(zone["ZoneStatus"])),
				replacedResource: replaced,
			})
		}
	}
	sort.Slice(zones, func(i, j int) bool {
		left := zones[i].endpointID + "\x00" + zones[i].zoneID
		right := zones[j].endpointID + "\x00" + zones[j].zoneID
		return left < right
	})
	return zones, requestID, nil
}

func deletionInProgressState(state string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(state))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "DELETING", "DELETE_IN_PROGRESS", "DELETION_IN_PROGRESS":
		return true
	default:
		return false
	}
}

func deletionInProgressResult(state, readbackRequestID string) contracts.ActionResult {
	return contracts.ActionResult{
		Data: map[string]any{
			"phase":               "deletion_in_progress",
			"state":               strings.TrimSpace(state),
			"readback_request_id": strings.TrimSpace(readbackRequestID),
		},
		RetryAfter: actionReadbackInterval,
	}
}

func dataWorksProjectDeletionInProgress(err error) (string, bool) {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) {
		return "", false
	}
	code := strings.TrimSpace(providerCall.Provider.Code)
	message := strings.TrimSpace(providerCall.Provider.Message)
	return providerCall.Provider.RequestID,
		code == "1101080161" ||
			(strings.Contains(message, "正在删除") && strings.Contains(message, "不可重复删除"))
}

func isProviderErrorCategory(err error, category execution.ErrorCategory) bool {
	var providerCall *contracts.ProviderCallError
	return errors.As(err, &providerCall) && providerCall.Provider.Category == category
}

type deletionPreparation struct {
	exists             bool
	protectionDisabled bool
	inProgress         bool
	state              string
	readRequestID      string
	data               map[string]any
}

func (h *ResourceAction) prepareDeletion(
	ctx context.Context,
	request contracts.ActionRequest,
) (deletionPreparation, error) {
	protection := h.action.DeletionProtection
	if protection == nil {
		return deletionPreparation{exists: true}, nil
	}
	read := h.action.Read
	if protection.Read != nil {
		read = protection.Read
	}
	readback, result, resource, err := h.readDetails(ctx, request, *read)
	if err != nil {
		return deletionPreparation{}, err
	}
	if !readback.Exists {
		return deletionPreparation{}, nil
	}
	preparation := deletionPreparation{
		exists:        true,
		state:         readback.State,
		readRequestID: result.RequestID,
	}
	if deletionInProgressState(readback.State) ||
		(h.nativeType == KMSKeyNativeType &&
			strings.EqualFold(strings.TrimSpace(readback.State), "PendingDeletion")) {
		preparation.inProgress = true
		if h.nativeType == KMSKeyNativeType {
			preparation.data = kmsScheduledDeletionData(resource)
		}
		return preparation, nil
	}
	actual := strings.TrimSpace(stringValue(valueAtPath(resource, protection.Path)))
	if !matchesAllowedValue(actual, protection.EnabledValues) {
		return preparation, nil
	}
	parameters, err := resolveSpecParameters(
		protection.Disable.Parameters,
		specParameterContext{
			region: h.region, nativeID: request.Asset.Identity.NativeID,
			nativeIDs:  []string{request.Asset.Identity.NativeID},
			normalized: request.Asset.Normalized,
		},
	)
	if err != nil {
		return deletionPreparation{}, err
	}
	_, err = h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    protection.Disable.Operation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   parameters,
		IdempotencyKey: request.IdempotencyKey +
			":disable-deletion-protection",
	})
	if err != nil {
		return deletionPreparation{}, err
	}
	preparation.protectionDisabled = true
	return preparation, nil
}

func (h *ResourceAction) deleteSLSProject(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	projectID := strings.TrimSpace(request.Asset.Identity.NativeID)
	project, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    slsGetProjectOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters:   map[string]any{"project": projectID},
	})
	if isNotFound(err) {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if returnedID := strings.TrimSpace(stringValue(project.Data["projectName"])); returnedID != projectID {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud SLS GetProject returned project %q, want %q",
			returnedID,
			projectID,
		)
	}
	deletionProtectionDisabled := false
	if boolValue(project.Data["deletionProtection"]) {
		if _, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    slsUpdateProjectOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"project":            projectID,
				"description":        stringValue(project.Data["description"]),
				"deletionProtection": false,
			},
			IdempotencyKey: request.IdempotencyKey + ":disable-deletion-protection",
		}); err != nil {
			return contracts.ActionResult{}, err
		}
		deletionProtectionDisabled = true
	}
	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   h.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": h.region},
		Parameters:     parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(result.Data)
	if data == nil {
		data = make(map[string]any)
	}
	if deletionProtectionDisabled {
		data["deletion_protection_disabled"] = true
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: data, RetryAfter: h.pollInterval(),
	}, nil
}

type armsEnvironmentFeature struct {
	name   string
	status string
}

func (h *ResourceAction) deleteARMSEnvironment(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	features, listResult, exists, err := h.listARMSEnvironmentFeatures(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !exists {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, nil
	}
	pending := make([]string, 0, len(features))
	requestID := listResult.RequestID
	operationID := listResult.OperationID
	for _, feature := range features {
		if strings.EqualFold(feature.status, "UnInstall") {
			continue
		}
		pending = append(pending, feature.name)
		if strings.EqualFold(feature.status, "UnInstalling") {
			continue
		}
		result, invokeErr := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    armsDeleteEnvironmentFeatureOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId": h.region, "EnvironmentId": request.Asset.Identity.NativeID,
				"FeatureName": feature.name,
			},
			IdempotencyKey: request.IdempotencyKey + ":delete-feature:" + feature.name,
		})
		if isNotFound(invokeErr) {
			continue
		}
		if invokeErr != nil {
			return contracts.ActionResult{}, invokeErr
		}
		if responseErr := armsResponseError(armsDeleteEnvironmentFeatureOperation, result); responseErr != nil {
			return contracts.ActionResult{}, responseErr
		}
		requestID = result.RequestID
		operationID = result.OperationID
	}
	if len(pending) == 0 {
		return h.invokeARMSEnvironmentDelete(ctx, request)
	}
	sort.Strings(pending)
	return contracts.ActionResult{
		ProviderRequestID: requestID, ProviderOperationID: operationID,
		Data: map[string]any{
			"phase": armsFeatureUninstallPhase, "pending_features": stringSliceAny(pending),
		},
		RetryAfter: h.pollInterval(),
	}, nil
}

func (h *ResourceAction) invokeARMSEnvironmentDelete(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID, Operation: operation,
		Scope: map[string]string{"region": h.region}, Parameters: parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if isNotFound(err) {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if responseErr := armsResponseError(operation, result); responseErr != nil {
		return contracts.ActionResult{}, responseErr
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: cloneTopologyMap(result.Data), RetryAfter: h.pollInterval(),
	}, nil
}

func (h *ResourceAction) listARMSEnvironmentFeatures(
	ctx context.Context,
	request contracts.ActionRequest,
) ([]armsEnvironmentFeature, contracts.InvocationResult, bool, error) {
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    armsListEnvironmentFeaturesOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"RegionId": h.region, "EnvironmentId": request.Asset.Identity.NativeID,
		},
	})
	if isNotFound(err) {
		return nil, contracts.InvocationResult{}, false, nil
	}
	if err != nil {
		return nil, contracts.InvocationResult{}, false, err
	}
	if responseErr := armsResponseError(armsListEnvironmentFeaturesOperation, result); responseErr != nil {
		return nil, contracts.InvocationResult{}, false, responseErr
	}
	rawFeatures, ok := productAPIListValue(result.Data["Data"])
	if !ok {
		if result.Data["Data"] == nil {
			rawFeatures = []any{}
		} else {
			return nil, contracts.InvocationResult{}, false, fmt.Errorf(
				"Alibaba Cloud ARMS ListEnvironmentFeatures returned an invalid Data collection",
			)
		}
	}
	features := make([]armsEnvironmentFeature, 0, len(rawFeatures))
	for _, raw := range rawFeatures {
		feature, objectOK := productAPIResourceMap(raw)
		if !objectOK {
			return nil, contracts.InvocationResult{}, false, fmt.Errorf(
				"Alibaba Cloud ARMS ListEnvironmentFeatures returned a non-object feature",
			)
		}
		if environmentID := strings.TrimSpace(stringValue(feature["EnvironmentId"])); environmentID != "" && environmentID != request.Asset.Identity.NativeID {
			return nil, contracts.InvocationResult{}, false, fmt.Errorf(
				"Alibaba Cloud ARMS ListEnvironmentFeatures returned environment %q, want %q",
				environmentID, request.Asset.Identity.NativeID,
			)
		}
		name := strings.TrimSpace(stringValue(feature["Name"]))
		if name == "" {
			return nil, contracts.InvocationResult{}, false, fmt.Errorf(
				"Alibaba Cloud ARMS ListEnvironmentFeatures returned a feature without a name",
			)
		}
		features = append(features, armsEnvironmentFeature{
			name: name, status: strings.TrimSpace(stringValue(feature["Status"])),
		})
	}
	sort.Slice(features, func(i, j int) bool { return features[i].name < features[j].name })
	return features, result, true, nil
}

func armsResponseError(operation string, result contracts.InvocationResult) error {
	code := strings.TrimSpace(stringValue(result.Data["Code"]))
	success, successPresent := result.Data["Success"].(bool)
	if (code == "" || code == "200") && (!successPresent || success) {
		return nil
	}
	if code == "" {
		code = "ARMSOperationFailed"
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorProviderFailure, Code: code,
		Message:   strings.TrimSpace(stringValue(result.Data["Message"])),
		RequestID: result.RequestID,
		Summary:   map[string]any{"operation": operation},
	}}
}

func stringSliceAny(values []string) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}

type nasLifecyclePolicy struct {
	id   string
	name string
}

func (h *ResourceAction) deleteNASLifecyclePolicies(
	ctx context.Context,
	request contracts.ActionRequest,
) error {
	const pageSize = 100
	fileSystemID := strings.TrimSpace(request.Asset.Identity.NativeID)
	policies := make([]nasLifecyclePolicy, 0)
	for page := 1; ; page++ {
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    "AlibabaCloud.NAS.DescribeLifecyclePolicies",
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"FileSystemId": fileSystemID,
				"PageNumber":   page,
				"PageSize":     pageSize,
			},
		})
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		rawPolicies := valueAtPath(result.Data, "LifecyclePolicies")
		items, ok := productAPIListValue(rawPolicies)
		if !ok {
			if total, totalOK := integerValue(result.Data["TotalCount"]); totalOK && total == 0 {
				items = []any{}
			} else {
				return fmt.Errorf("Alibaba Cloud NAS DescribeLifecyclePolicies returned an invalid LifecyclePolicies collection")
			}
		}
		for _, item := range items {
			policy, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("Alibaba Cloud NAS DescribeLifecyclePolicies returned a non-object lifecycle policy")
			}
			returnedFileSystemID := strings.TrimSpace(stringValue(policy["FileSystemId"]))
			if returnedFileSystemID != "" && returnedFileSystemID != fileSystemID {
				return fmt.Errorf(
					"Alibaba Cloud NAS DescribeLifecyclePolicies returned policy for file system %q, want %q",
					returnedFileSystemID,
					fileSystemID,
				)
			}
			policyID := strings.TrimSpace(stringValue(policy["LifecyclePolicyId"]))
			policyName := strings.TrimSpace(stringValue(policy["LifecyclePolicyName"]))
			if policyID == "" && policyName == "" {
				return fmt.Errorf("Alibaba Cloud NAS lifecycle policy has neither an ID nor a name")
			}
			policies = append(policies, nasLifecyclePolicy{id: policyID, name: policyName})
		}
		total, hasTotal := integerValue(result.Data["TotalCount"])
		if len(items) == 0 || hasTotal && page*pageSize >= total || !hasTotal && len(items) < pageSize {
			break
		}
	}
	sort.Slice(policies, func(i, j int) bool {
		left := policies[i].name
		if left == "" {
			left = policies[i].id
		}
		right := policies[j].name
		if right == "" {
			right = policies[j].id
		}
		return left < right
	})
	for _, policy := range policies {
		parameters := map[string]any{"FileSystemId": fileSystemID}
		identity := policy.id
		if policy.name != "" {
			parameters["LifecyclePolicyName"] = policy.name
			identity = policy.name
		} else {
			parameters["LifecyclePolicyId"] = policy.id
		}
		_, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    "AlibabaCloud.NAS.DeleteLifecyclePolicy",
			Scope:        map[string]string{"region": h.region},
			Parameters:   parameters,
			IdempotencyKey: request.IdempotencyKey +
				":delete-lifecycle-policy:" + identity,
		})
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}

func dataWorksResourceGroupAlreadyDeleted(data map[string]any) bool {
	if strings.TrimSpace(stringValue(data["Code"])) != "704203" {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(stringValue(data["Message"])))
	return strings.Contains(message, "resource group status is deleted")
}

func (h *ResourceAction) detachDataDisk(ctx context.Context, request contracts.ActionRequest, instanceID string) (contracts.ActionResult, error) {
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    "DetachDisk",
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"DiskId": request.Asset.Identity.NativeID, "InstanceId": instanceID,
			"DeleteWithInstance": false,
		},
		IdempotencyKey: request.IdempotencyKey + ":detach",
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(result.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = "detach"
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: data, RetryAfter: h.pollInterval(),
	}, nil
}

func (h *ResourceAction) pollInterval() time.Duration {
	if h.action.PollIntervalSeconds > 0 {
		return time.Duration(h.action.PollIntervalSeconds) * time.Second
	}
	return actionReadbackInterval
}

func (h *ResourceAction) DeletionCheckTimeout() time.Duration {
	return time.Duration(h.action.DeletionCheckTimeoutSeconds) * time.Second
}

func (h *ResourceAction) deleteInvocation(request contracts.ActionRequest) (string, map[string]any, error) {
	parameters, err := h.actionParameters(request)
	if err != nil {
		return "", nil, err
	}
	if h.nativeType == PrometheusNativeType &&
		strings.EqualFold(
			normalizedActionValue(request.Asset.Normalized, "ClusterType"),
			"cloud-product-prometheus",
		) {
		return "AlibabaCloud.UninstallPromCluster", map[string]any{
			"RegionId":  h.region,
			"ClusterId": request.Asset.Identity.NativeID,
		}, nil
	}
	return h.action.Operation, parameters, nil
}

func (h *ResourceAction) unsupportedCleanupError(value asset.Asset) error {
	var code, message string
	summary := map[string]any{
		"operation":   h.action.Operation,
		"skip_reason": string(asset.SkipProductUnsupported),
	}
	switch h.nativeType {
	case KMSKeyNativeType:
		creator, managed := serviceManagedKMSCreator(value.Normalized)
		if !managed {
			return nil
		}
		code = "CleanupUnsupported.ServiceManagedKMSKey"
		message = fmt.Sprintf("the KMS key is managed by the %s cloud service and cannot be deleted directly", creator)
		summary["creator"] = creator
	case "ACS::ECS::NetworkInterface":
		interfaceType := normalizedActionValue(value.Normalized, "Type")
		if strings.EqualFold(interfaceType, "Primary") {
			code = "CleanupUnsupported.PrimaryNetworkInterface"
			message = "primary network interfaces are deleted with their owning instance and cannot be deleted directly"
			summary["network_interface_type"] = interfaceType
			break
		}
		description := normalizedActionValue(value.Normalized, "Description")
		name := normalizedActionValue(value.Normalized, "NetworkInterfaceName")
		if !strings.EqualFold(description, "created by NAS") ||
			!strings.HasPrefix(strings.ToLower(name), "extreme-") {
			return nil
		}
		code = "CleanupUnsupported.NASManagedNetworkInterface"
		message = "the network interface is managed by an Extreme NAS file system and cannot be deleted directly"
		summary["network_interface_type"] = interfaceType
		summary["description"] = description
	case "ACS::HBR::Vault":
		statistics := valueAtPath(value.Normalized, "configuration.BackupPlanStatistics")
		if !nonEmptyNormalizedCollection(statistics) {
			return nil
		}
		code = "CleanupUnsupported.BackupPlanBoundVault"
		message = "backup vaults bound to backup plans cannot be deleted directly"
		summary["backup_plan_statistics"] = statistics
	default:
		return nil
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorUnsupported,
		Code:     code,
		Message:  message,
		Summary:  summary,
	}}
}

func nonEmptyNormalizedCollection(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) > 0
	case []map[string]any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return false
	}
}

func serviceManagedKMSCreator(normalized map[string]any) (string, bool) {
	creator := normalizedActionValue(normalized, "creator")
	if creator == "" {
		creator = normalizedActionValue(normalized, "Creator")
	}
	return creator, creator != "" && !isDecimalIdentifier(creator)
}

func normalizedActionValue(normalized map[string]any, field string) string {
	if value := strings.TrimSpace(stringValue(valueAtPath(normalized, field))); value != "" {
		return value
	}
	return strings.TrimSpace(stringValue(valueAtPath(normalized, "configuration."+field)))
}

func isDecimalIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (h *ResourceAction) rosStackGroupOperationResults(
	ctx context.Context,
	operationID string,
) ([]map[string]any, string, error) {
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    rosListStackGroupOperationResultsOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"RegionId": h.region, "OperationId": operationID,
			"PageNumber": 1, "PageSize": 50,
		},
	})
	if err != nil {
		return nil, result.RequestID, err
	}
	items, ok := productAPIListValue(result.Data["StackGroupOperationResults"])
	if !ok && result.Data["StackGroupOperationResults"] != nil {
		return nil, result.RequestID, fmt.Errorf(
			"Alibaba Cloud ROS ListStackGroupOperationResults returned an invalid result collection",
		)
	}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		operationResult, itemOK := productAPIResourceMap(item)
		if !itemOK {
			return nil, result.RequestID, fmt.Errorf(
				"Alibaba Cloud ROS ListStackGroupOperationResults returned a non-object result",
			)
		}
		results = append(results, operationResult)
	}
	return results, result.RequestID, nil
}

func (h *ResourceAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if h.nativeType == KMSKeyNativeType {
		return contracts.WaitResult{
			Done: true, State: KMSDeletionScheduledState,
			Data: cloneTopologyMap(result.Data),
		}, nil
	}
	if h.nativeType == OSSBucketNativeType && ossBucketCleanupPhase(
		strings.TrimSpace(stringValue(result.Data["phase"])),
	) {
		next, err := h.advanceOSSBucketCleanup(ctx, request, result.Data)
		if err != nil {
			if ossHDFSSafeModeMayBeEnabled(result.Data) &&
				!isProviderErrorCategory(err, execution.ErrorRetryable) {
				err = h.restoreOSSHDFSAfterFailure(ctx, request, err)
			}
			return contracts.WaitResult{}, err
		}
		phase := strings.TrimSpace(stringValue(next.Data["phase"]))
		if phase == ossBucketDeleteRequestedPhase {
			return contracts.WaitResult{
				Done:  true,
				State: phase,
				Data:  cloneTopologyMap(next.Data),
			}, nil
		}
		retryAfter := next.RetryAfter
		if retryAfter <= 0 {
			retryAfter = h.pollInterval()
		}
		return contracts.WaitResult{
			Done:       false,
			RetryAfter: retryAfter,
			State:      phase,
			Data:       cloneTopologyMap(next.Data),
		}, nil
	}
	if h.nativeType == ROSStackGroupNativeType &&
		strings.TrimSpace(stringValue(result.Data["phase"])) == rosDeleteStackInstancesPhase {
		operationID := strings.TrimSpace(stringValue(result.Data["operation_id"]))
		if operationID == "" {
			operationID = strings.TrimSpace(result.ProviderOperationID)
		}
		if operationID == "" {
			return contracts.WaitResult{}, fmt.Errorf(
				"Alibaba Cloud ROS stack instance deletion is missing OperationId",
			)
		}
		operation, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    rosGetStackGroupOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId": h.region, "OperationId": operationID,
			},
		})
		if err != nil {
			return contracts.WaitResult{}, err
		}
		status := strings.TrimSpace(stringValue(valueAtPath(operation.Data, "StackGroupOperation.Status")))
		switch strings.ToUpper(status) {
		case "RUNNING", "STOPPING":
			data := cloneTopologyMap(result.Data)
			if data == nil {
				data = make(map[string]any)
			}
			data["operation_status"] = status
			data["operation_read_request_id"] = operation.RequestID
			return contracts.WaitResult{
				Done: false, RetryAfter: h.pollInterval(), State: status, Data: data,
			}, nil
		case "SUCCEEDED":
			next, advanceErr := h.advanceROSStackGroupDeletion(ctx, request)
			if advanceErr != nil {
				return contracts.WaitResult{}, advanceErr
			}
			nextPhase := strings.TrimSpace(stringValue(next.Data["phase"]))
			if nextPhase == "absent" || nextPhase == rosDeleteStackGroupRequestedPhase {
				return contracts.WaitResult{
					Done: true, State: nextPhase, Data: cloneTopologyMap(next.Data),
				}, nil
			}
			return contracts.WaitResult{
				Done: false, RetryAfter: next.RetryAfter,
				State: nextPhase, Data: cloneTopologyMap(next.Data),
			}, nil
		case "FAILED", "STOPPED":
			reason := strings.TrimSpace(stringValue(valueAtPath(operation.Data, "StackGroupOperation.StatusReason")))
			failureSummary := map[string]any{
				"operation_id": operationID,
				"stack_group":  request.Asset.Identity.NativeID,
			}
			results, resultsRequestID, resultsErr := h.rosStackGroupOperationResults(ctx, operationID)
			if resultsErr == nil {
				failureSummary["operation_results"] = results
				failureSummary["operation_results_request_id"] = resultsRequestID
				for _, item := range results {
					itemReason := strings.TrimSpace(stringValue(item["StatusReason"]))
					if itemReason != "" {
						reason = itemReason
						break
					}
				}
			} else {
				failureSummary["operation_results_error"] = resultsErr.Error()
			}
			if reason == "" {
				reason = fmt.Sprintf("stack group operation %s finished in state %s", operationID, status)
			}
			return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
				Category: execution.ErrorProviderFailure, Code: status, Message: reason,
				RequestID: operation.RequestID,
				Summary:   failureSummary,
			}}
		default:
			return contracts.WaitResult{}, fmt.Errorf(
				"Alibaba Cloud ROS stack group operation %q returned unsupported status %q",
				operationID,
				status,
			)
		}
	}
	if h.nativeType == endpointServiceNativeType {
		phase := strings.TrimSpace(stringValue(result.Data["phase"]))
		if phase == "disconnect_endpoint_connections" || phase == "detach_service_resources" {
			next, err := h.advancePrivateLinkEndpointServiceDeletion(ctx, request)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			nextPhase := strings.TrimSpace(stringValue(next.Data["phase"]))
			if nextPhase == "absent" || nextPhase == "endpoint_service_delete_requested" {
				return contracts.WaitResult{
					Done: true, State: nextPhase, Data: cloneTopologyMap(next.Data),
				}, nil
			}
			return contracts.WaitResult{
				Done: false, RetryAfter: next.RetryAfter,
				State: nextPhase, Data: cloneTopologyMap(next.Data),
			}, nil
		}
	}
	if _, boundResource := privateLinkServiceResourceType(h.nativeType); boundResource {
		phase := strings.TrimSpace(stringValue(result.Data["phase"]))
		if phase == privateLinkDisconnectBoundZonePhase ||
			phase == privateLinkDetachBoundResourcePhase {
			next, handled, err := h.advancePrivateLinkServiceResourceCleanup(ctx, request)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if handled {
				return contracts.WaitResult{
					Done: false, RetryAfter: next.RetryAfter,
					State: strings.TrimSpace(stringValue(next.Data["phase"])),
					Data:  cloneTopologyMap(next.Data),
				}, nil
			}
			operation, parameters, err := h.deleteInvocation(request)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			deleted, err := h.provider.Invoke(ctx, contracts.Invocation{
				ConnectionID: h.connectionID, Operation: operation,
				Scope: map[string]string{"region": h.region}, Parameters: parameters,
				IdempotencyKey: request.IdempotencyKey,
			})
			if err != nil {
				return contracts.WaitResult{}, err
			}
			data := cloneTopologyMap(deleted.Data)
			if data == nil {
				data = make(map[string]any)
			}
			data["phase"] = privateLinkBoundResourceDeletePhase
			data["provider_request_id"] = deleted.RequestID
			return contracts.WaitResult{
				Done: true, State: privateLinkBoundResourceDeletePhase, Data: data,
			}, nil
		}
	}
	if h.nativeType == ECSImageNativeType {
		switch strings.TrimSpace(stringValue(result.Data["phase"])) {
		case imageVisibilityChangePhase, imageSharePermissionChangePhase:
			return h.waitImagePreparation(ctx, request, result)
		}
	}
	if h.nativeType == diskNativeType {
		return h.waitDisk(ctx, request, result)
	}
	if h.nativeType == vpcNativeType {
		switch strings.TrimSpace(stringValue(result.Data["phase"])) {
		case vpcDetachDhcpOptionsSetPhase:
			return h.waitVPCDhcpOptionsSetDetach(ctx, request, result)
		case vpcDeleteDhcpOptionsSetPhase:
			return h.waitVPCDhcpOptionsSetDelete(ctx, request, result)
		}
	}
	if h.nativeType == ARMSEnvironmentNativeType &&
		strings.TrimSpace(stringValue(result.Data["phase"])) == armsFeatureUninstallPhase {
		return h.waitARMSEnvironmentFeatures(ctx, request, result)
	}
	readback, err := h.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: readback.State}, nil
	}
	if h.failureState(readback.State) {
		return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorProviderFailure,
			Code:     readback.State,
			Message:  fmt.Sprintf("Alibaba Cloud %s deletion finished in failure state %s", h.nativeType, readback.State),
			Summary: map[string]any{
				"native_id": request.Asset.Identity.NativeID,
				"state":     readback.State,
			},
		}}
	}
	if h.terminalState(readback.State) {
		return contracts.WaitResult{Done: true, State: readback.State}, nil
	}
	return contracts.WaitResult{Done: false, RetryAfter: h.pollInterval(), State: readback.State}, nil
}

func ossHDFSSafeModeMayBeEnabled(data map[string]any) bool {
	return boolValue(data["hdfs_safe_mode_requested"]) ||
		boolValue(data["hdfs_safe_mode"])
}

func ossBucketCleanupPhase(phase string) bool {
	switch phase {
	case ossDeleteObjectVersionsPhase,
		ossDeleteCurrentObjectsPhase,
		ossAbortMultipartUploadsPhase,
		ossDeleteLiveChannelsPhase,
		ossRetryDeleteBucketPhase,
		ossPauseHDFSPhase,
		ossWaitHDFSPausedPhase,
		ossDeleteHDFSFilesPhase,
		ossRestoreHDFSPhase,
		ossWaitHDFSRestoredPhase,
		ossDisableDataLakeStoragePhase,
		ossWaitDataLakeCleanupPhase:
		return true
	default:
		return false
	}
}

func (h *ResourceAction) detachVPCDhcpOptionsSet(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, bool, error) {
	readback, readResult, resource, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	if !readback.Exists {
		return contracts.ActionResult{
			Data: map[string]any{"phase": "absent"}, RetryAfter: h.pollInterval(),
		}, true, nil
	}
	dhcpOptionsSetID := strings.TrimSpace(stringValue(resource["DhcpOptionsSetId"]))
	if dhcpOptionsSetID == "" {
		// A continued execution can resume after the asynchronous detach has
		// already cleared DescribeVpcs. Preserve the scanned association so the
		// DHCP options set is still deleted before the VPC on that retry.
		dhcpOptionsSetID = normalizedActionValue(request.Asset.Normalized, "dhcpOptionsSetId")
		if dhcpOptionsSetID == "" {
			return contracts.ActionResult{}, false, nil
		}
		deleteResult, deleteErr := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    vpcDeleteDhcpOptionsSetOperation,
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId": h.region, "DhcpOptionsSetId": dhcpOptionsSetID,
			},
			IdempotencyKey: request.IdempotencyKey + ":delete-dhcp-options-set:" + dhcpOptionsSetID,
		})
		if isNotFound(deleteErr) {
			return contracts.ActionResult{}, false, nil
		}
		if deleteErr != nil {
			return contracts.ActionResult{}, false, deleteErr
		}
		return contracts.ActionResult{
			ProviderRequestID: deleteResult.RequestID,
			Data: map[string]any{
				"phase":                              vpcDeleteDhcpOptionsSetPhase,
				"dhcp_options_set_id":                dhcpOptionsSetID,
				"dhcp_options_set_delete_request_id": deleteResult.RequestID,
				"vpc_readback_request_id":            readResult.RequestID,
			},
			RetryAfter: h.pollInterval(),
		}, true, nil
	}
	dhcpOptionsSetStatus := strings.TrimSpace(stringValue(resource["DhcpOptionsSetStatus"]))
	data := map[string]any{
		"phase":                   vpcDetachDhcpOptionsSetPhase,
		"dhcp_options_set_id":     dhcpOptionsSetID,
		"dhcp_options_set_status": dhcpOptionsSetStatus,
		"vpc_readback_request_id": readResult.RequestID,
	}
	if strings.EqualFold(dhcpOptionsSetStatus, "Pending") {
		return contracts.ActionResult{
			ProviderRequestID: readResult.RequestID,
			Data:              data,
			RetryAfter:        h.pollInterval(),
		}, true, nil
	}
	detachResult, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    vpcDetachDhcpOptionsSetOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"RegionId": h.region, "VpcId": request.Asset.Identity.NativeID,
			"DhcpOptionsSetId": dhcpOptionsSetID,
		},
		IdempotencyKey: request.IdempotencyKey + ":detach-dhcp-options-set",
	})
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	data["detach_request_id"] = detachResult.RequestID
	return contracts.ActionResult{
		ProviderRequestID: detachResult.RequestID,
		Data:              data,
		RetryAfter:        h.pollInterval(),
	}, true, nil
}

func (h *ResourceAction) waitVPCDhcpOptionsSetDetach(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	readback, readResult, resource, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data := cloneTopologyMap(result.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["vpc_readback_request_id"] = readResult.RequestID
	if !readback.Exists {
		data["phase"] = "absent"
		return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
	}
	dhcpOptionsSetID := strings.TrimSpace(stringValue(resource["DhcpOptionsSetId"]))
	if dhcpOptionsSetID != "" {
		dhcpOptionsSetStatus := strings.TrimSpace(stringValue(resource["DhcpOptionsSetStatus"]))
		data["dhcp_options_set_id"] = dhcpOptionsSetID
		data["dhcp_options_set_status"] = dhcpOptionsSetStatus
		state := "detaching_dhcp_options_set"
		if dhcpOptionsSetStatus != "" {
			state += ":" + dhcpOptionsSetStatus
		}
		return contracts.WaitResult{
			Done: false, RetryAfter: h.pollInterval(), State: state, Data: data,
		}, nil
	}
	dhcpOptionsSetID = strings.TrimSpace(stringValue(data["dhcp_options_set_id"]))
	if dhcpOptionsSetID == "" {
		return contracts.WaitResult{}, fmt.Errorf(
			"Alibaba Cloud VPC %s DHCP options set detach lost the options set ID",
			request.Asset.Identity.NativeID,
		)
	}
	deleteResult, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    vpcDeleteDhcpOptionsSetOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"RegionId": h.region, "DhcpOptionsSetId": dhcpOptionsSetID,
		},
		IdempotencyKey: request.IdempotencyKey + ":delete-dhcp-options-set:" + dhcpOptionsSetID,
	})
	if isNotFound(err) {
		return h.invokeVPCDeleteAfterDhcpOptionsSet(ctx, request, data)
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data["phase"] = vpcDeleteDhcpOptionsSetPhase
	data["dhcp_options_set_delete_request_id"] = deleteResult.RequestID
	return contracts.WaitResult{
		Done: false, RetryAfter: h.pollInterval(), State: "deleting_dhcp_options_set", Data: data,
	}, nil
}

func (h *ResourceAction) waitVPCDhcpOptionsSetDelete(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	data := cloneTopologyMap(result.Data)
	if data == nil {
		data = make(map[string]any)
	}
	dhcpOptionsSetID := strings.TrimSpace(stringValue(data["dhcp_options_set_id"]))
	if dhcpOptionsSetID == "" {
		return contracts.WaitResult{}, fmt.Errorf(
			"Alibaba Cloud VPC %s DHCP options set deletion lost the options set ID",
			request.Asset.Identity.NativeID,
		)
	}
	listResult, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    vpcListDhcpOptionsSetsOperation,
		Scope:        map[string]string{"region": h.region},
		Parameters: map[string]any{
			"RegionId": h.region, "DhcpOptionsSetId.1": dhcpOptionsSetID, "MaxResults": 1,
		},
	})
	if isNotFound(err) {
		return h.invokeVPCDeleteAfterDhcpOptionsSet(ctx, request, data)
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data["dhcp_options_set_readback_request_id"] = listResult.RequestID
	resource, exists, err := readbackResource(
		listResult.Data,
		[]string{"DhcpOptionsSets"},
		"DhcpOptionsSetId",
		dhcpOptionsSetID,
	)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if exists {
		state := strings.TrimSpace(stringValue(resource["Status"]))
		data["dhcp_options_set_status"] = state
		if state == "" {
			state = "deleting"
		}
		return contracts.WaitResult{
			Done: false, RetryAfter: h.pollInterval(),
			State: "deleting_dhcp_options_set:" + state, Data: data,
		}, nil
	}
	return h.invokeVPCDeleteAfterDhcpOptionsSet(ctx, request, data)
}

func (h *ResourceAction) invokeVPCDeleteAfterDhcpOptionsSet(
	ctx context.Context,
	request contracts.ActionRequest,
	data map[string]any,
) (contracts.WaitResult, error) {
	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	deleteResult, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   h.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": h.region},
		Parameters:     parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if isNotFound(err) {
		data["phase"] = "absent"
		return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data["phase"] = "vpc_delete_requested"
	data["delete_request_id"] = deleteResult.RequestID
	return contracts.WaitResult{Done: true, State: "delete_requested", Data: data}, nil
}

func (h *ResourceAction) waitImagePreparation(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	readback, readResult, resource, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data := cloneTopologyMap(result.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["visibility_readback_request_id"] = readResult.RequestID
	if !readback.Exists {
		data["phase"] = "absent"
		return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
	}
	if boolValue(resource["IsPublic"]) {
		return contracts.WaitResult{
			Done: false, RetryAfter: h.pollInterval(),
			State: "image_visibility_change_pending", Data: data,
		}, nil
	}
	sharePreparation, err := h.removeImageShareAccounts(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !sharePreparation.exists {
		data["phase"] = "absent"
		return contracts.WaitResult{Done: true, State: "absent", Data: data}, nil
	}
	data["share_permission_readback_request_id"] = sharePreparation.readRequestID
	if sharePreparation.changed {
		data["phase"] = imageSharePermissionChangePhase
		data["image_share_accounts_removed"] = sharePreparation.removedCount
		data["image_share_accounts_remaining"] = sharePreparation.remaining
		data["share_permission_request_id"] = sharePreparation.requestID
		return contracts.WaitResult{
			Done: false, RetryAfter: h.pollInterval(),
			State: "image_share_permission_change_pending", Data: data,
		}, nil
	}

	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	deleteResult, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   h.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": h.region},
		Parameters:     parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data["phase"] = "image_delete_requested"
	data["delete_request_id"] = deleteResult.RequestID
	data["delete_operation_id"] = deleteResult.OperationID
	return contracts.WaitResult{
		Done: true, State: "image_delete_requested", Data: data,
	}, nil
}

func (h *ResourceAction) waitARMSEnvironmentFeatures(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	features, _, exists, err := h.listARMSEnvironmentFeatures(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	pending := make([]string, 0, len(features))
	failed := make([]string, 0)
	for _, feature := range features {
		if strings.EqualFold(feature.status, "UnInstall") {
			continue
		}
		pending = append(pending, feature.name)
		if strings.EqualFold(feature.status, "UnInstallFailed") {
			failed = append(failed, feature.name)
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorProviderFailure,
			Code:     "ARMSFeatureUninstallFailed",
			Message:  "one or more ARMS environment features failed to uninstall",
			Summary: map[string]any{
				"environment_id": request.Asset.Identity.NativeID,
				"features":       stringSliceAny(failed),
			},
		}}
	}
	if len(pending) > 0 {
		sort.Strings(pending)
		data := cloneTopologyMap(result.Data)
		if data == nil {
			data = make(map[string]any)
		}
		data["pending_features"] = stringSliceAny(pending)
		return contracts.WaitResult{
			Done: false, RetryAfter: h.pollInterval(), State: "features_uninstalling", Data: data,
		}, nil
	}
	deleted, err := h.invokeARMSEnvironmentDelete(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data := cloneTopologyMap(deleted.Data)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = "environment_delete_requested"
	if deleted.ProviderRequestID != "" {
		data["environment_delete_request_id"] = deleted.ProviderRequestID
	}
	return contracts.WaitResult{Done: true, State: "delete_requested", Data: data}, nil
}

func (h *ResourceAction) waitDisk(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	phase, _ := result.Data["phase"].(string)
	if phase != "detach" {
		readback, err := h.Readback(ctx, request)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if !readback.Exists {
			return contracts.WaitResult{Done: true, State: readback.State}, nil
		}
		return contracts.WaitResult{Done: false, RetryAfter: h.pollInterval(), State: readback.State}, nil
	}
	readback, _, resource, err := h.readbackDetails(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: readback.State}, nil
	}
	if strings.TrimSpace(stringValue(resource["InstanceId"])) != "" || strings.EqualFold(readback.State, "In_use") {
		return contracts.WaitResult{Done: false, RetryAfter: h.pollInterval(), State: "detaching"}, nil
	}
	operation, parameters, err := h.deleteInvocation(request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	_, err = h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID, Operation: operation,
		Scope: map[string]string{"region": h.region}, Parameters: parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if isNotFound(err) {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: true, State: "delete_requested"}, nil
}

func (h *ResourceAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := h.validate(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if h.nativeType == KMSKeyNativeType {
		readback, _, resource, err := h.readbackDetails(ctx, request)
		if err != nil || !readback.Exists {
			return readback, err
		}
		if !strings.EqualFold(strings.TrimSpace(readback.State), "PendingDeletion") {
			return readback, nil
		}
		return contracts.ReadbackResult{
			Exists: false,
			State:  KMSDeletionScheduledState,
			Data:   kmsScheduledDeletionData(resource),
		}, nil
	}
	readback, _, err := h.readback(ctx, request)
	if err == nil && readback.Exists && h.terminalState(readback.State) {
		readback.Exists = false
	}
	if err == nil && !readback.Exists && h.nativeType == nlbLoadBalancerNativeType {
		return h.readbackNLBManagedEIPs(ctx, request, readback)
	}
	return readback, err
}

func kmsScheduledDeletionData(resource map[string]any) map[string]any {
	data := map[string]any{"pending_window_days": 7}
	if scheduledDeletionAt := strings.TrimSpace(
		stringValue(valueAtPath(resource, "DeleteDate")),
	); scheduledDeletionAt != "" {
		data["scheduled_deletion_at"] = scheduledDeletionAt
	}
	return data
}

func (h *ResourceAction) readbackNLBManagedEIPs(
	ctx context.Context,
	request contracts.ActionRequest,
	nlbReadback contracts.ReadbackResult,
) (contracts.ReadbackResult, error) {
	ids := normalizedActionStrings(
		request.Asset.Normalized[NormalizedNLBEIPIDsField],
	)
	if len(ids) == 0 {
		return nlbReadback, nil
	}
	pending := make([]string, 0)
	requestIDs := make(map[string]string, len(ids))
	for _, allocationID := range ids {
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID,
			Operation:    "DescribeEipAddresses",
			Scope:        map[string]string{"region": h.region},
			Parameters: map[string]any{
				"RegionId": h.region, "AllocationId": allocationID, "PageSize": 1,
			},
		})
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		resource, exists, err := readbackResource(
			result.Data,
			[]string{"EipAddresses", "EipAddress"},
			"AllocationId",
			allocationID,
		)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if !exists || !isNLBManagedEIPRecord(resource, request.Asset.Identity.NativeID) {
			continue
		}
		pending = append(pending, allocationID)
		requestIDs[allocationID] = result.RequestID
	}
	if len(pending) == 0 {
		return nlbReadback, nil
	}
	return contracts.ReadbackResult{
		Exists: true,
		State:  "managed_eips_pending",
		Data: map[string]any{
			"managed_eip_ids": pending, "provider_request_ids": requestIDs,
		},
	}, nil
}

func normalizedActionStrings(value any) []string {
	seen := make(map[string]struct{})
	appendValue := func(raw any) {
		if text := strings.TrimSpace(stringValue(raw)); text != "" {
			seen[text] = struct{}{}
		}
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			appendValue(item)
		}
	case []string:
		for _, item := range typed {
			appendValue(item)
		}
	default:
		appendValue(value)
	}
	result := make([]string, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func (h *ResourceAction) readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, contracts.InvocationResult, error) {
	readback, result, _, err := h.readbackDetails(ctx, request)
	return readback, result, err
}

func (h *ResourceAction) readbackDetails(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, contracts.InvocationResult, map[string]any, error) {
	return h.readDetails(ctx, request, *h.action.Read)
}

func (h *ResourceAction) readDetails(
	ctx context.Context,
	request contracts.ActionRequest,
	read spec.ProductAPISpec,
) (contracts.ReadbackResult, contracts.InvocationResult, map[string]any, error) {
	parameters, err := resolveSpecParameters(
		read.Parameters,
		specParameterContext{
			region:     h.region,
			nativeID:   request.Asset.Identity.NativeID,
			nativeIDs:  []string{request.Asset.Identity.NativeID},
			normalized: request.Asset.Normalized,
		},
	)
	if err != nil {
		return contracts.ReadbackResult{}, contracts.InvocationResult{}, nil, err
	}
	result, err := h.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: h.connectionID,
		Operation:    read.Operation, Scope: map[string]string{"region": h.region},
		Parameters: parameters,
	})
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false, State: "absent"}, contracts.InvocationResult{}, nil, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, contracts.InvocationResult{}, nil, err
	}
	absent, err := classifyReadbackResponseEnvelope(read.Operation, result.Data)
	if err != nil {
		return contracts.ReadbackResult{}, result, nil, err
	}
	if absent {
		return contracts.ReadbackResult{
			Exists: false,
			State:  "absent",
			Data:   result.Data,
		}, result, nil, nil
	}
	resource, exists, err := readbackResource(
		result.Data,
		strings.Split(read.ItemsPath, "."),
		read.IdentityPath,
		readbackIdentity(request.Asset),
	)
	if err != nil {
		return contracts.ReadbackResult{}, result, nil, err
	}
	if !exists {
		return contracts.ReadbackResult{Exists: false, State: "absent", Data: result.Data}, result, nil, nil
	}
	state := stringValue(valueAtPath(resource, read.StatePath))
	return contracts.ReadbackResult{Exists: true, State: state, Data: result.Data}, result, resource, nil
}

// classifyReadbackResponseEnvelope handles product gateways that return an
// error envelope as a successful invocation result. OSS GetBucketInfo uses
// ecCode 0015-00000101 when the requested bucket no longer exists.
func classifyReadbackResponseEnvelope(operation string, data map[string]any) (bool, error) {
	code := strings.TrimSpace(stringValue(valueAtPath(data, "ecCode")))
	if code == "" {
		return false, nil
	}
	if operation == ossGetBucketInfoOperation && code == ossBucketNotFoundEnvelopeCode {
		return true, nil
	}
	return false, fmt.Errorf(
		"Alibaba Cloud readback operation %q returned ecCode %q",
		operation,
		code,
	)
}

func readbackIdentity(value asset.Asset) string {
	if value.Identity.NativeType == CENChildInstanceAttachmentNativeType {
		if childInstanceID := strings.TrimSpace(
			stringValue(value.Normalized["childInstanceId"]),
		); childInstanceID != "" {
			return childInstanceID
		}
	}
	return value.Identity.NativeID
}

func matchesAllowedValue(actual string, allowed []string) bool {
	for _, value := range allowed {
		if strings.EqualFold(actual, strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func (h *ResourceAction) actionParameters(request contracts.ActionRequest) (map[string]any, error) {
	configured := clonedParameters(h.action.Parameters)
	for name, value := range request.Parameters {
		current, allowed := configured[name]
		if !allowed {
			return nil, fmt.Errorf("Alibaba Cloud delete parameter %q is not allowed by the resource spec", name)
		}
		if expression, ok := current.(string); ok &&
			(strings.HasPrefix(expression, "resource.") || strings.HasPrefix(expression, "scope.")) {
			return nil, fmt.Errorf("Alibaba Cloud delete parameter %q is managed by the resource spec", name)
		}
		if !sameParameterType(current, value) {
			return nil, fmt.Errorf("Alibaba Cloud delete parameter %q has type %T, want %T", name, value, current)
		}
		configured[name] = value
	}
	resolved, err := resolveSpecParameters(
		configured,
		specParameterContext{
			region: h.region, nativeID: request.Asset.Identity.NativeID,
			nativeIDs:  []string{request.Asset.Identity.NativeID},
			normalized: request.Asset.Normalized,
		},
	)
	if err != nil {
		return nil, err
	}
	for _, name := range h.action.RequiredParams {
		value, ok := resolved[name]
		if !ok || strings.TrimSpace(stringValue(value)) == "" {
			return nil, fmt.Errorf(
				"Alibaba Cloud delete parameter %q must be supplied with a non-empty value",
				name,
			)
		}
	}
	return resolved, nil
}

func sameParameterType(expected, actual any) bool {
	switch expected.(type) {
	case bool:
		_, ok := actual.(bool)
		return ok
	case string:
		_, ok := actual.(string)
		return ok
	case int, int32, int64, float32, float64:
		switch actual.(type) {
		case int, int32, int64, float32, float64:
			return true
		}
		return false
	default:
		return fmt.Sprintf("%T", expected) == fmt.Sprintf("%T", actual)
	}
}

func (h *ResourceAction) terminalState(state string) bool {
	if h.action.Waiter != "terminal" {
		return false
	}
	for _, terminal := range h.action.TerminalStates {
		if strings.EqualFold(strings.TrimSpace(terminal), strings.TrimSpace(state)) {
			return true
		}
	}
	return false
}

func (h *ResourceAction) failureState(state string) bool {
	if h.action.Waiter != "terminal" {
		return false
	}
	for _, failure := range h.action.FailureStates {
		if strings.EqualFold(strings.TrimSpace(failure), strings.TrimSpace(state)) {
			return true
		}
	}
	return false
}

func (h *ResourceAction) validate(request contracts.ActionRequest) error {
	if request.Asset.Identity.Provider != asset.ProviderAliCloud || request.Asset.Identity.NativeType != h.nativeType {
		return fmt.Errorf("Alibaba Cloud action asset type does not match hook type %q", h.nativeType)
	}
	if request.Action != "delete" {
		return fmt.Errorf("unsupported Alibaba Cloud action %q", request.Action)
	}
	if strings.TrimSpace(request.Asset.Identity.NativeID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("Alibaba Cloud delete requires native ID and idempotency key")
	}
	return nil
}

func readbackResource(
	data map[string]any,
	path []string,
	identityPath string,
	wantID string,
) (map[string]any, bool, error) {
	if len(path) == 0 {
		return validateReadbackIdentity(data, identityPath, wantID)
	}
	if len(path) == 1 && path[0] == "$" {
		return validateReadbackIdentity(data, identityPath, wantID)
	}
	current := valueAtPath(data, strings.Join(path, "."))
	if resource, ok := productAPIResourceMap(current); ok {
		return validateReadbackIdentity(resource, identityPath, wantID)
	}
	items, ok := productAPIListValue(current)
	if !ok {
		return nil, false, nil
	}
	foundResource := false
	for _, item := range items {
		resource, ok := productAPIResourceMap(item)
		if !ok {
			continue
		}
		foundResource = true
		identity := strings.TrimSpace(stringValue(valueAtPath(resource, identityPath)))
		if identity == strings.TrimSpace(wantID) {
			return resource, true, nil
		}
	}
	if foundResource {
		return nil, false, fmt.Errorf(
			"Alibaba Cloud readback returned resources but none matched native ID %q",
			wantID,
		)
	}
	return nil, false, nil
}

func validateReadbackIdentity(
	resource map[string]any,
	identityPath string,
	wantID string,
) (map[string]any, bool, error) {
	identity := strings.TrimSpace(stringValue(valueAtPath(resource, identityPath)))
	if identity == strings.TrimSpace(wantID) {
		return resource, true, nil
	}
	return nil, false, fmt.Errorf(
		"Alibaba Cloud readback returned native ID %q, want %q",
		identity,
		wantID,
	)
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var providerError *contracts.ProviderCallError
	return errors.As(err, &providerError) && providerError.Provider.Category == execution.ErrorNotFound
}

const (
	aliKafkaGetInstancesOperation = "AlibabaCloud.AliKafka.GetInstanceList"
	aliKafkaReleaseOperation      = "AlibabaCloud.AliKafka.ReleaseInstance"
	aliKafkaDeleteOperation       = "AlibabaCloud.AliKafka.DeleteInstance"
	aliKafkaReleasedState         = "6"
	aliKafkaReleasingState        = "23"
)

type AliKafkaAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type aliKafkaInstance struct {
	exists   bool
	state    string
	paidType int
	data     map[string]any
	request  string
}

func NewAliKafkaHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*AliKafkaAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud AliKafka action requires connection ID and region")
	}
	return &AliKafkaAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

func (a *AliKafkaAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateAliKafkaAction(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"native_id": request.Asset.Identity.NativeID, "state": instance.state,
		"paid_type": instance.paidType, "provider_request_id": instance.request,
	}
	if !instance.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	if deletionInProgressState(instance.state) {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence,
		}, nil
	}
	if instance.state == aliKafkaReleasedState {
		evidence["next_operation"] = aliKafkaDeleteOperation
		return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
	}
	if aliKafkaPostpaid(instance.paidType) {
		evidence["next_operation"] = aliKafkaReleaseOperation
		return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
	}
	return contracts.PreflightResult{
		Allowed:  false,
		Reason:   "prepaid AliKafka instances must be unsubscribed, expire, or be released through the billing lifecycle before their released record can be deleted",
		Evidence: evidence,
	}, nil
}

func (a *AliKafkaAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateAliKafkaAction(request); err != nil {
		return contracts.ActionResult{}, err
	}
	force, err := aliKafkaForceDelete(request.Parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !instance.exists {
		return contracts.ActionResult{
			ProviderRequestID: instance.request, Data: map[string]any{"phase": "absent"},
		}, nil
	}
	if instance.state == aliKafkaReleasedState {
		return a.invokeAliKafka(
			ctx,
			request,
			aliKafkaDeleteOperation,
			map[string]any{
				"InstanceId": request.Asset.Identity.NativeID,
				"RegionId":   a.region,
			},
			"delete_record",
		)
	}
	if !aliKafkaPostpaid(instance.paidType) {
		return contracts.ActionResult{}, aliKafkaPrepaidProtected(
			instance.paidType,
			instance.state,
		)
	}
	if instance.state == aliKafkaReleasingState {
		return contracts.ActionResult{
			ProviderRequestID: instance.request,
			Data:              map[string]any{"phase": "release", "already_releasing": true},
		}, nil
	}
	return a.invokeAliKafka(
		ctx,
		request,
		aliKafkaReleaseOperation,
		map[string]any{
			"InstanceId":          request.Asset.Identity.NativeID,
			"RegionId":            a.region,
			"ForceDeleteInstance": force,
		},
		"release",
	)
}

func (a *AliKafkaAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	if err := validateAliKafkaAction(request); err != nil {
		return contracts.WaitResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !instance.exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	phase, _ := result.Data["phase"].(string)
	if phase == "delete_record" {
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval, State: instance.state,
		}, nil
	}
	if instance.state != aliKafkaReleasedState {
		if !aliKafkaPostpaid(instance.paidType) {
			return contracts.WaitResult{}, aliKafkaPrepaidProtected(
				instance.paidType,
				instance.state,
			)
		}
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval, State: instance.state,
		}, nil
	}
	_, err = a.invokeAliKafka(
		ctx,
		request,
		aliKafkaDeleteOperation,
		map[string]any{
			"InstanceId": request.Asset.Identity.NativeID,
			"RegionId":   a.region,
		},
		"delete_record",
	)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: true, State: "delete_requested"}, nil
}

func (a *AliKafkaAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateAliKafkaAction(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if !instance.exists {
		return contracts.ReadbackResult{
			Exists: false, State: "absent", Data: instance.data,
		}, nil
	}
	return contracts.ReadbackResult{
		Exists: true, State: instance.state, Data: instance.data,
	}, nil
}

func (a *AliKafkaAction) readInstance(
	ctx context.Context,
	nativeID string,
) (aliKafkaInstance, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    aliKafkaGetInstancesOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "InstanceId": []string{nativeID},
		},
	})
	if isNotFound(err) {
		return aliKafkaInstance{data: map[string]any{}}, nil
	}
	if err != nil {
		return aliKafkaInstance{}, err
	}
	raw := valueAtPath(result.Data, "InstanceList.InstanceVO")
	if raw == nil {
		return aliKafkaInstance{data: result.Data, request: result.RequestID}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return aliKafkaInstance{}, fmt.Errorf(
			"Alibaba Cloud AliKafka instance list response is not an array",
		)
	}
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if !ok ||
			strings.TrimSpace(stringValue(resource["InstanceId"])) != strings.TrimSpace(nativeID) {
			continue
		}
		paidType, ok := integerValue(resource["PaidType"])
		if !ok {
			return aliKafkaInstance{}, fmt.Errorf(
				"Alibaba Cloud AliKafka instance %q has an invalid PaidType",
				nativeID,
			)
		}
		return aliKafkaInstance{
			exists: true, state: stringValue(resource["ViewInstanceStatusCode"]),
			paidType: paidType, data: result.Data, request: result.RequestID,
		}, nil
	}
	if len(items) > 0 {
		return aliKafkaInstance{}, fmt.Errorf(
			"Alibaba Cloud AliKafka readback returned instances but none matched native ID %q",
			nativeID,
		)
	}
	return aliKafkaInstance{data: result.Data, request: result.RequestID}, nil
}

func (a *AliKafkaAction) invokeAliKafka(
	ctx context.Context,
	request contracts.ActionRequest,
	operation string,
	parameters map[string]any,
	phase string,
) (contracts.ActionResult, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   a.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": a.region},
		Parameters:     parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := make(map[string]any, len(result.Data)+1)
	for key, value := range result.Data {
		data[key] = value
	}
	data["phase"] = phase
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: data,
	}, nil
}

func validateAliKafkaAction(request contracts.ActionRequest) error {
	if request.Asset.Identity.Provider != asset.ProviderAliCloud ||
		request.Asset.Identity.NativeType != AliKafkaInstanceNativeType {
		return fmt.Errorf(
			"Alibaba Cloud action asset type does not match hook type %q",
			AliKafkaInstanceNativeType,
		)
	}
	if request.Action != "delete" {
		return fmt.Errorf("unsupported Alibaba Cloud action %q", request.Action)
	}
	if strings.TrimSpace(request.Asset.Identity.NativeID) == "" ||
		strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("Alibaba Cloud AliKafka delete requires native ID and idempotency key")
	}
	return nil
}

func aliKafkaForceDelete(parameters map[string]any) (bool, error) {
	for name, value := range parameters {
		if name != "ForceDeleteInstance" {
			return false, fmt.Errorf(
				"Alibaba Cloud AliKafka delete parameter %q is not allowed by the resource spec",
				name,
			)
		}
		force, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf(
				"Alibaba Cloud AliKafka delete parameter %q has type %T, want bool",
				name,
				value,
			)
		}
		return force, nil
	}
	return false, nil
}

func aliKafkaPostpaid(paidType int) bool {
	return paidType == 1 || paidType == 3
}

func aliKafkaPrepaidProtected(paidType int, state string) error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorProtected,
		Message:  "prepaid AliKafka instance requires billing lifecycle release",
		Summary: map[string]any{
			"paid_type": paidType, "state": state,
			"required_action": "unsubscribe, expire, or release the subscription before deleting the released instance record",
		},
	}}
}

type DeleteBehavior struct {
	DeleteByDefault bool `json:"delete_by_default"`
	Changeable      bool `json:"changeable"`
}

type ClusterResource struct {
	ClusterID      string         `json:"cluster_id"`
	InstanceID     string         `json:"instance_id"`
	ResourceType   string         `json:"resource_type"`
	State          string         `json:"state"`
	AutoCreate     *int           `json:"auto_create,omitempty"`
	CreatorType    string         `json:"creator_type,omitempty"`
	DeleteBehavior DeleteBehavior `json:"delete_behavior"`
	ResourceInfo   string         `json:"resource_info,omitempty"`
}

type ClusterNode struct {
	InstanceID  string `json:"instance_id"`
	NodePoolID  string `json:"nodepool_id"`
	Source      string `json:"source"`
	AutoCreated *bool  `json:"auto_created,omitempty"`
}

type DeleteOption struct {
	ResourceType string `json:"resource_type"`
	DeleteMode   string `json:"delete_mode"`
}

type DeleteClusterRequest struct {
	ClusterID          string         `json:"cluster_id"`
	RetainAllResources bool           `json:"retain_all_resources"`
	RetainResources    []string       `json:"retain_resources,omitempty"`
	DeleteOptions      []DeleteOption `json:"delete_options,omitempty"`
}

type DeleteClusterResponse struct {
	ClusterID string `json:"cluster_id"`
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
}

type ACKClusterDetail struct {
	ClusterID          string
	State              string
	DeletionProtection bool
}

type ACKClient interface {
	DescribeClusterResources(context.Context, string, bool) ([]ClusterResource, string, error)
	DescribeClusterNodes(context.Context, string) ([]ClusterNode, string, error)
	DeleteCluster(context.Context, DeleteClusterRequest) (DeleteClusterResponse, error)
}

type ACKDeletionProtectionClient interface {
	DescribeClusterDetail(context.Context, string) (ACKClusterDetail, string, error)
	ModifyClusterDeletionProtection(context.Context, string, bool) (string, error)
}

type ACKAction struct {
	client ACKClient
}

func NewACKHook(client ACKClient) *ACKAction {
	return &ACKAction{client: client}
}

func (a *ACKAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := validateACKAction(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	if a == nil || a.client == nil {
		return contracts.PreflightResult{}, fmt.Errorf("ACK client is required")
	}
	protectionClient, ok := a.client.(ACKDeletionProtectionClient)
	if !ok {
		return contracts.PreflightResult{}, fmt.Errorf(
			"ACK client does not support cluster detail queries",
		)
	}
	detail, detailRequestID, err := protectionClient.DescribeClusterDetail(
		ctx,
		request.Asset.Identity.NativeID,
	)
	if isNotFound(err) {
		return contracts.PreflightResult{
			Absent: true,
			Reason: "resource no longer exists",
			Evidence: map[string]any{
				"cluster_id":          request.Asset.Identity.NativeID,
				"provider_request_id": detailRequestID,
			},
		}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, NormalizeError(err)
	}
	if strings.TrimSpace(detail.ClusterID) != request.Asset.Identity.NativeID {
		return contracts.PreflightResult{}, fmt.Errorf(
			"ACK cluster detail returned cluster %q, want %q",
			detail.ClusterID,
			request.Asset.Identity.NativeID,
		)
	}
	if deletionInProgressState(detail.State) {
		return contracts.PreflightResult{
			Absent: true,
			Reason: "resource deletion is already in progress",
			Evidence: map[string]any{
				"cluster_id":          request.Asset.Identity.NativeID,
				"state":               detail.State,
				"provider_request_id": detailRequestID,
			},
		}, nil
	}
	resources, requestID, err := a.client.DescribeClusterResources(ctx, request.Asset.Identity.NativeID, true)
	if err != nil {
		return contracts.PreflightResult{}, NormalizeError(err)
	}
	return contracts.PreflightResult{
		Allowed: true,
		Evidence: map[string]any{
			"cluster_id": request.Asset.Identity.NativeID, "state": detail.State,
			"deletion_protection":       detail.DeletionProtection,
			"associated_resource_count": len(resources), "request_id": requestID,
			"detail_request_id": detailRequestID,
		},
	}, nil
}

func (a *ACKAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := validateACKAction(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if a == nil || a.client == nil {
		return contracts.ActionResult{}, fmt.Errorf("ACK client is required")
	}
	protectionClient, ok := a.client.(ACKDeletionProtectionClient)
	if !ok {
		return contracts.ActionResult{}, fmt.Errorf(
			"ACK client does not support cluster deletion protection",
		)
	}
	detail, detailRequestID, err := protectionClient.DescribeClusterDetail(
		ctx,
		request.Asset.Identity.NativeID,
	)
	if isNotFound(err) {
		return contracts.ActionResult{
			ProviderRequestID: detailRequestID,
			Data:              map[string]any{"phase": "absent"},
		}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	if strings.TrimSpace(detail.ClusterID) != request.Asset.Identity.NativeID {
		return contracts.ActionResult{}, fmt.Errorf(
			"ACK cluster detail returned cluster %q, want %q",
			detail.ClusterID,
			request.Asset.Identity.NativeID,
		)
	}
	protectionDisabled := false
	if detail.DeletionProtection {
		if _, err := protectionClient.ModifyClusterDeletionProtection(
			ctx,
			request.Asset.Identity.NativeID,
			false,
		); err != nil {
			return contracts.ActionResult{}, NormalizeError(err)
		}
		protectionDisabled = true
	}
	deleteRequest, err := mapDeleteRequest(request.Asset.Identity.NativeID, request.Parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.DeleteCluster(ctx, deleteRequest)
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	data := map[string]any{"cluster_id": response.ClusterID}
	if protectionDisabled {
		data["deletion_protection_disabled"] = true
	}
	return contracts.ActionResult{
		ProviderRequestID: response.RequestID, ProviderOperationID: response.TaskID,
		Data: data,
	}, nil
}

func (a *ACKAction) Wait(ctx context.Context, request contracts.ActionRequest, _ contracts.ActionResult) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: readback.State}, nil
	}
	return contracts.WaitResult{Done: false, RetryAfter: actionReadbackInterval, State: readback.State}, nil
}

func (a *ACKAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := validateACKAction(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a == nil || a.client == nil {
		return contracts.ReadbackResult{}, fmt.Errorf("ACK client is required")
	}
	resources, requestID, err := a.client.DescribeClusterResources(ctx, request.Asset.Identity.NativeID, true)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false, State: "absent"}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, NormalizeError(err)
	}
	return contracts.ReadbackResult{
		Exists: true, State: "present",
		Data: map[string]any{"provider_request_id": requestID, "associated_resource_count": len(resources)},
	}, nil
}

func validateACKAction(request contracts.ActionRequest) error {
	if request.Asset.Identity.Provider != asset.ProviderAliCloud || request.Asset.Identity.NativeType != ACKClusterNativeType {
		return fmt.Errorf("ACK action requires an Alibaba Cloud ACK cluster asset")
	}
	if request.Action != "delete" {
		return fmt.Errorf("unsupported ACK action %q", request.Action)
	}
	if request.Asset.Identity.NativeID == "" || request.IdempotencyKey == "" {
		return fmt.Errorf("ACK delete requires cluster ID and idempotency key")
	}
	return nil
}

func mapDeleteRequest(clusterID string, parameters map[string]any) (DeleteClusterRequest, error) {
	request := DeleteClusterRequest{ClusterID: clusterID}
	if value, exists := parameters["retain_all_resources"]; exists {
		flag, ok := value.(bool)
		if !ok {
			return DeleteClusterRequest{}, fmt.Errorf("retain_all_resources must be boolean")
		}
		request.RetainAllResources = flag
	}
	if value, exists := parameters["retain_resources"]; exists {
		resources, err := stringSlice(value)
		if err != nil {
			return DeleteClusterRequest{}, fmt.Errorf("retain_resources: %w", err)
		}
		request.RetainResources = resources
	}
	if value, exists := parameters["delete_options"]; exists {
		options, err := deleteOptions(value)
		if err != nil {
			return DeleteClusterRequest{}, err
		}
		request.DeleteOptions = options
	}
	if request.RetainAllResources {
		request.RetainResources = nil
	}
	sort.Strings(request.RetainResources)
	sort.Slice(request.DeleteOptions, func(i, j int) bool {
		return request.DeleteOptions[i].ResourceType < request.DeleteOptions[j].ResourceType
	})
	return request, nil
}

func stringSlice(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || text == "" {
				return nil, fmt.Errorf("must contain non-empty strings")
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("must be an array of strings")
	}
}

var allowedDeleteResourceTypes = map[string]struct{}{
	"SLB": {}, "ALB": {}, "SLS_Data": {}, "SLS_ControlPlane": {}, "PrivateZone": {},
}

func deleteOptions(value any) ([]DeleteOption, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("delete_options must be an array")
	}
	result := make([]DeleteOption, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("delete_options entries must be objects")
		}
		resourceType, _ := object["resource_type"].(string)
		deleteMode, _ := object["delete_mode"].(string)
		if _, allowed := allowedDeleteResourceTypes[resourceType]; !allowed {
			return nil, fmt.Errorf("delete_options resource_type %q is unsupported", resourceType)
		}
		if deleteMode != "delete" && deleteMode != "retain" {
			return nil, fmt.Errorf("delete_options delete_mode %q is unsupported", deleteMode)
		}
		if _, duplicate := seen[resourceType]; duplicate {
			return nil, fmt.Errorf("duplicate delete_options resource_type %q", resourceType)
		}
		seen[resourceType] = struct{}{}
		result = append(result, DeleteOption{ResourceType: resourceType, DeleteMode: deleteMode})
	}
	return result, nil
}
