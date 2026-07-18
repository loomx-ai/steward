package alicloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	BPStudioApplicationNativeType = "ACS::BPStudio::Application"

	bpStudioGetApplicationOperation     = "AlibabaCloud.BPStudio.GetApplication"
	bpStudioReleaseApplicationOperation = "AlibabaCloud.BPStudio.ReleaseApplication"
	bpStudioDeleteApplicationOperation  = "AlibabaCloud.BPStudio.DeleteApplication"
	bpStudioDeleteApplicationRegion     = "cn-hangzhou"
	bpStudioDestroyedSuccessState       = "Destroyed_Success"
	bpStudioDestroyedFailureState       = "Destroyed_Failure"
)

type BPStudioAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type bpStudioApplication struct {
	exists    bool
	status    string
	requestID string
	data      map[string]any
}

func NewBPStudioHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*BPStudioAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud BPStudio action requires connection ID and region")
	}
	return &BPStudioAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

func (a *BPStudioAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, BPStudioApplicationNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	application, err := a.readApplication(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"native_id":           request.Asset.Identity.NativeID,
		"state":               application.status,
		"provider_request_id": application.requestID,
	}
	if !application.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	if deletionInProgressState(application.status) {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence,
		}, nil
	}
	if bpStudioTransitionInProgress(application.status) {
		evidence["next_operation"] = bpStudioGetApplicationOperation
	} else if bpStudioCanDeleteDirectly(application.status) {
		evidence["next_operation"] = bpStudioDeleteApplicationOperation
	} else if bpStudioRequiresRelease(application.status) {
		evidence["next_operation"] = bpStudioReleaseApplicationOperation
	} else {
		return contracts.PreflightResult{}, bpStudioUnsupportedDeleteState(application.status)
	}
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *BPStudioAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, BPStudioApplicationNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	application, err := a.readApplication(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !application.exists {
		return contracts.ActionResult{
			ProviderRequestID: application.requestID,
			Data:              map[string]any{"phase": "absent"},
		}, nil
	}
	if bpStudioCanDeleteDirectly(application.status) {
		return a.deleteApplication(ctx, request)
	}
	if bpStudioTransitionInProgress(application.status) {
		return contracts.ActionResult{
			ProviderRequestID: application.requestID,
			Data: map[string]any{
				"phase": "transition", "provider_state": application.status,
			},
		}, nil
	}
	if bpStudioRequiresRelease(application.status) {
		return a.invoke(
			ctx, request, bpStudioReleaseApplicationOperation,
			map[string]any{"ApplicationId": request.Asset.Identity.NativeID},
			"release",
		)
	}
	return contracts.ActionResult{}, bpStudioUnsupportedDeleteState(application.status)
}

func (a *BPStudioAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	if err := validateComplexDelete(request, BPStudioApplicationNativeType); err != nil {
		return contracts.WaitResult{}, err
	}
	application, err := a.readApplication(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !application.exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	if application.status == bpStudioDestroyedFailureState {
		return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorConflict,
			Message:  "BPStudio application resource release failed",
			Summary: map[string]any{
				"application_id": request.Asset.Identity.NativeID,
				"state":          application.status,
			},
		}}
	}
	phase := stringValue(result.Data["phase"])
	if application.status == bpStudioDestroyedSuccessState && phase != "delete" {
		if _, err := a.deleteApplication(ctx, request); err != nil {
			return contracts.WaitResult{}, err
		}
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval, State: "delete_requested",
		}, nil
	}
	if phase == "transition" && !bpStudioTransitionInProgress(application.status) {
		state := "delete_requested"
		if bpStudioRequiresRelease(application.status) {
			state = "release_requested"
			if _, err := a.invoke(
				ctx, request, bpStudioReleaseApplicationOperation,
				map[string]any{"ApplicationId": request.Asset.Identity.NativeID},
				"release",
			); err != nil {
				return contracts.WaitResult{}, err
			}
		} else {
			if !bpStudioCanDeleteDirectly(application.status) {
				return contracts.WaitResult{}, bpStudioUnsupportedDeleteState(application.status)
			}
			if _, err := a.deleteApplication(ctx, request); err != nil {
				return contracts.WaitResult{}, err
			}
		}
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval, State: state,
		}, nil
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: actionReadbackInterval, State: application.status,
	}, nil
}

func (a *BPStudioAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, BPStudioApplicationNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	application, err := a.readApplication(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{
		Exists: application.exists, State: application.status, Data: application.data,
	}, nil
}

func (a *BPStudioAction) readApplication(
	ctx context.Context,
	nativeID string,
) (bpStudioApplication, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    bpStudioGetApplicationOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters:   map[string]any{"ApplicationId": nativeID},
	})
	if isNotFound(err) || isBPStudioApplicationDeleted(err) {
		return bpStudioApplication{}, nil
	}
	if err != nil {
		return bpStudioApplication{}, err
	}
	if isBPStudioApplicationDeletedResponse(result.Data) {
		return bpStudioApplication{requestID: result.RequestID, data: result.Data}, nil
	}
	if code := strings.TrimSpace(stringValue(result.Data["Code"])); code != "" && code != "200" {
		message := strings.TrimSpace(stringValue(result.Data["Message"]))
		if message == "" {
			message = "BPStudio GetApplication returned an unsuccessful response"
		}
		return bpStudioApplication{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorProviderFailure,
			Code:      code,
			Message:   message,
			RequestID: result.RequestID,
		}}
	}
	data, ok := result.Data["Data"].(map[string]any)
	if !ok || strings.TrimSpace(stringValue(data["ApplicationId"])) == "" {
		return bpStudioApplication{
			requestID: result.RequestID, data: result.Data,
		}, nil
	}
	if got := strings.TrimSpace(stringValue(data["ApplicationId"])); got != strings.TrimSpace(nativeID) {
		return bpStudioApplication{}, fmt.Errorf(
			"Alibaba Cloud BPStudio readback returned application %q, want %q",
			got,
			nativeID,
		)
	}
	return bpStudioApplication{
		exists: true, status: stringValue(data["Status"]),
		requestID: result.RequestID, data: result.Data,
	}, nil
}

func isBPStudioApplicationDeletedResponse(data map[string]any) bool {
	return strings.TrimSpace(stringValue(data["Code"])) == "8004"
}

func bpStudioRequiresRelease(status string) bool {
	switch strings.TrimSpace(status) {
	case "Deployed_Failure",
		"Partially_Deployed_Success",
		"Deployed_Success",
		"Destroyed_Failure",
		"Partially_Destroyed_Success",
		"Revised",
		"Verifying_In_Revision",
		"Verified_Failure_In_Revision",
		"Verified_Success_In_Revision",
		"Valuating_In_Revision",
		"Valuating_Failure_In_Revision",
		"Valuating_Success_In_Revision":
		return true
	default:
		return false
	}
}

func bpStudioCanDeleteDirectly(status string) bool {
	switch strings.TrimSpace(status) {
	case "Modified",
		"Verified_Failure",
		"Verified_Success",
		"Valuating_Failure",
		"Valuating_Success",
		bpStudioDestroyedSuccessState:
		return true
	default:
		return false
	}
}

func bpStudioTransitionInProgress(status string) bool {
	switch strings.TrimSpace(status) {
	case "Creating",
		"Verifying",
		"Valuating",
		"Deploying",
		"Destroying",
		"Delayed_Destroy",
		"Verifying_In_Revision",
		"Valuating_In_Revision":
		return true
	default:
		return false
	}
}

func bpStudioUnsupportedDeleteState(status string) error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorConflict,
		Code:     "BPStudio.ApplicationStateNotReady",
		Message:  fmt.Sprintf("BPStudio application state %q is not ready for safe deletion", status),
		Summary:  map[string]any{"state": strings.TrimSpace(status)},
	}}
}

func isBPStudioApplicationDeleted(err error) bool {
	var providerError *contracts.ProviderCallError
	return errors.As(err, &providerError) &&
		strings.TrimSpace(providerError.Provider.Code) == "8004"
}

func (a *BPStudioAction) deleteApplication(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	return a.invoke(
		ctx,
		request,
		bpStudioDeleteApplicationOperation,
		map[string]any{
			"ApplicationId": request.Asset.Identity.NativeID,
			"Force":         true,
		},
		"delete",
	)
}

func (a *BPStudioAction) invoke(
	ctx context.Context,
	request contracts.ActionRequest,
	operation string,
	parameters map[string]any,
	phase string,
) (contracts.ActionResult, error) {
	region := a.region
	if operation == bpStudioDeleteApplicationOperation {
		region = bpStudioDeleteApplicationRegion
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   a.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": region},
		Parameters:     parameters,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := clonedParameters(result.Data)
	data["phase"] = phase
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID, Data: data,
	}, nil
}

func validateComplexDelete(request contracts.ActionRequest, nativeType string) error {
	if request.Asset.Identity.Provider != asset.ProviderAliCloud ||
		request.Asset.Identity.NativeType != nativeType {
		return fmt.Errorf("Alibaba Cloud action requires native type %q", nativeType)
	}
	if request.Action != "delete" {
		return fmt.Errorf("unsupported Alibaba Cloud action %q", request.Action)
	}
	if strings.TrimSpace(request.Asset.Identity.NativeID) == "" ||
		strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("Alibaba Cloud delete requires native ID and idempotency key")
	}
	return nil
}

const (
	CloudFirewallInstanceNativeType = "ACS::CloudFirewall::Instance"

	cloudFirewallDescribeOperation       = "AlibabaCloud.CloudFirewall.DescribeUserBuyVersion"
	cloudFirewallReleasePostOperation    = "AlibabaCloud.CloudFirewall.ReleasePostInstance"
	cloudFirewallReleaseExpiredOperation = "AlibabaCloud.CloudFirewall.ReleaseExpiredInstance"
	cloudFirewallPostpaidVersion         = 10
)

type CloudFirewallAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
	now          func() time.Time
}

type cloudFirewallInstance struct {
	exists    bool
	version   int
	expireAt  int64
	status    string
	requestID string
	data      map[string]any
}

func NewCloudFirewallHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*CloudFirewallAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud Cloud Firewall action requires connection ID and region")
	}
	return &CloudFirewallAction{
		provider: provider, connectionID: connectionID,
		region: strings.TrimSpace(region), now: time.Now,
	}, nil
}

func (a *CloudFirewallAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, CloudFirewallInstanceNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"native_id":           request.Asset.Identity.NativeID,
		"version":             instance.version,
		"state":               instance.status,
		"expire_at":           instance.expireAt,
		"provider_request_id": instance.requestID,
	}
	if !instance.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	switch {
	case strings.EqualFold(instance.status, "deleting"):
		return contracts.PreflightResult{
			Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence,
		}, nil
	case instance.version == cloudFirewallPostpaidVersion:
		evidence["next_operation"] = cloudFirewallReleasePostOperation
		return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
	case a.expired(instance):
		evidence["next_operation"] = cloudFirewallReleaseExpiredOperation
		return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
	default:
		return contracts.PreflightResult{
			Allowed:  false,
			Reason:   "active subscription Cloud Firewall instances must be unsubscribed or expire before product API release",
			Evidence: evidence,
		}, nil
	}
}

func (a *CloudFirewallAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, CloudFirewallInstanceNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !instance.exists {
		return contracts.ActionResult{
			ProviderRequestID: instance.requestID, Data: map[string]any{"phase": "absent"},
		}, nil
	}
	if strings.EqualFold(instance.status, "deleting") {
		return contracts.ActionResult{
			ProviderRequestID: instance.requestID, Data: map[string]any{"phase": "release"},
		}, nil
	}
	operation := ""
	switch {
	case instance.version == cloudFirewallPostpaidVersion:
		operation = cloudFirewallReleasePostOperation
	case a.expired(instance):
		operation = cloudFirewallReleaseExpiredOperation
	default:
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorProtected,
			Message:  "active subscription Cloud Firewall instance cannot be released by product API",
			Summary: map[string]any{
				"instance_id": request.Asset.Identity.NativeID,
				"version":     instance.version,
				"expire_at":   instance.expireAt,
			},
		}}
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   a.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": a.region},
		Parameters:     map[string]any{"InstanceId": request.Asset.Identity.NativeID},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: map[string]any{"phase": "release", "operation": operation},
	}, nil
}

func (a *CloudFirewallAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	_ contracts.ActionResult,
) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: actionReadbackInterval, State: readback.State,
	}, nil
}

func (a *CloudFirewallAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, CloudFirewallInstanceNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	state := instance.status
	if !instance.exists {
		state = "absent"
	}
	return contracts.ReadbackResult{
		Exists: instance.exists, State: state, Data: instance.data,
	}, nil
}

func (a *CloudFirewallAction) expired(instance cloudFirewallInstance) bool {
	return instance.expireAt > 0 && instance.expireAt <= a.now().UnixMilli()
}

func (a *CloudFirewallAction) readInstance(
	ctx context.Context,
	nativeID string,
) (cloudFirewallInstance, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    cloudFirewallDescribeOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters:   map[string]any{"InstanceId": nativeID},
	})
	if isNotFound(err) {
		return cloudFirewallInstance{}, nil
	}
	if err != nil {
		return cloudFirewallInstance{}, err
	}
	got := strings.TrimSpace(stringValue(result.Data["InstanceId"]))
	status := strings.TrimSpace(stringValue(result.Data["InstanceStatus"]))
	if got == "" || strings.EqualFold(status, "free") {
		return cloudFirewallInstance{
			status: status, requestID: result.RequestID, data: result.Data,
		}, nil
	}
	if got != strings.TrimSpace(nativeID) {
		return cloudFirewallInstance{}, fmt.Errorf(
			"Alibaba Cloud Cloud Firewall readback returned instance %q, want %q",
			got,
			nativeID,
		)
	}
	version, versionOK := integerValue(result.Data["Version"])
	expireAt, expireOK := integerValue(result.Data["Expire"])
	if !versionOK {
		return cloudFirewallInstance{}, fmt.Errorf(
			"Alibaba Cloud Cloud Firewall instance %q has an invalid Version",
			nativeID,
		)
	}
	if !expireOK {
		expireAt = 0
	}
	return cloudFirewallInstance{
		exists: true, version: version, expireAt: int64(expireAt), status: status,
		requestID: result.RequestID, data: result.Data,
	}, nil
}

const (
	DdosBgpInstanceNativeType = "ACS::DdosBgp::Instance"

	ddosBgpDescribeOperation = "AlibabaCloud.DdosBgp.DescribeInstanceList"
	ddosBgpBillOperation     = "AlibabaCloud.DdosBgp.DescribeDdosOriginInstanceBill"
	ddosBgpReleaseOperation  = "AlibabaCloud.DdosBgp.ReleaseDdosOriginInstance"
)

type DdosBgpAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type ddosBgpInstance struct {
	exists    bool
	status    string
	requestID string
	data      map[string]any
}

func NewDdosBgpHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*DdosBgpAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud DDoS Origin action requires connection ID and region")
	}
	return &DdosBgpAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

func (a *DdosBgpAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, DdosBgpInstanceNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"native_id":           request.Asset.Identity.NativeID,
		"state":               instance.status,
		"provider_request_id": instance.requestID,
	}
	if !instance.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	if deletionInProgressState(instance.status) {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence,
		}, nil
	}
	postpaid, billRequestID, err := a.isPostpaid(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence["billing_request_id"] = billRequestID
	if !postpaid {
		return contracts.PreflightResult{
			Allowed:  false,
			Reason:   "only pay-as-you-go Anti-DDoS Origin instances can be released through the product API",
			Evidence: evidence,
		}, nil
	}
	evidence["next_operation"] = ddosBgpReleaseOperation
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *DdosBgpAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, DdosBgpInstanceNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !instance.exists {
		return contracts.ActionResult{
			ProviderRequestID: instance.requestID, Data: map[string]any{"phase": "absent"},
		}, nil
	}
	postpaid, _, err := a.isPostpaid(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !postpaid {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorProtected,
			Message:  "subscription Anti-DDoS Origin instance cannot be released by product API",
			Summary:  map[string]any{"instance_id": request.Asset.Identity.NativeID},
		}}
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   a.connectionID,
		Operation:      ddosBgpReleaseOperation,
		Scope:          map[string]string{"region": a.region},
		Parameters:     map[string]any{"InstanceId": request.Asset.Identity.NativeID},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: map[string]any{"phase": "release"},
	}, nil
}

func (a *DdosBgpAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	_ contracts.ActionResult,
) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: actionReadbackInterval, State: readback.State,
	}, nil
}

func (a *DdosBgpAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, DdosBgpInstanceNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	state := instance.status
	if !instance.exists {
		state = "absent"
	}
	return contracts.ReadbackResult{
		Exists: instance.exists, State: state, Data: instance.data,
	}, nil
}

func (a *DdosBgpAction) readInstance(
	ctx context.Context,
	nativeID string,
) (ddosBgpInstance, error) {
	encoded, err := json.Marshal([]string{nativeID})
	if err != nil {
		return ddosBgpInstance{}, err
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    ddosBgpDescribeOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"InstanceIdList": string(encoded), "PageNo": 1, "PageSize": 1,
		},
	})
	if isNotFound(err) {
		return ddosBgpInstance{}, nil
	}
	if err != nil {
		return ddosBgpInstance{}, err
	}
	items, ok := result.Data["InstanceList"].([]any)
	if !ok || len(items) == 0 {
		return ddosBgpInstance{requestID: result.RequestID, data: result.Data}, nil
	}
	for _, item := range items {
		resource, ok := item.(map[string]any)
		if !ok {
			continue
		}
		got := strings.TrimSpace(stringValue(resource["InstanceId"]))
		if got == strings.TrimSpace(nativeID) {
			return ddosBgpInstance{
				exists: true, status: stringValue(resource["Status"]),
				requestID: result.RequestID, data: result.Data,
			}, nil
		}
	}
	return ddosBgpInstance{}, fmt.Errorf(
		"Alibaba Cloud DDoS Origin readback returned instances but none matched %q",
		nativeID,
	)
}

func (a *DdosBgpAction) isPostpaid(
	ctx context.Context,
	nativeID string,
) (bool, string, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    ddosBgpBillOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters:   map[string]any{"IsShowList": false},
	})
	if isNotFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return strings.TrimSpace(stringValue(result.Data["InstanceId"])) ==
		strings.TrimSpace(nativeID), result.RequestID, nil
}

const (
	ENSInstanceNativeType = "ACS::ENS::Instance"

	ensDescribeOperation       = "AlibabaCloud.ENS.DescribeInstances"
	ensRenewalOperation        = "AlibabaCloud.ENS.DescribeInstanceAutoRenewAttribute"
	ensReleasePostOperation    = "AlibabaCloud.ENS.ReleasePostPaidInstance"
	ensReleasePrepaidOperation = "AlibabaCloud.ENS.ReleasePrePaidInstance"
)

type ENSAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type ensInstance struct {
	exists    bool
	status    string
	requestID string
	data      map[string]any
}

func NewENSHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*ENSAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud ENS action requires connection ID and region")
	}
	return &ENSAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

func (a *ENSAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, ENSInstanceNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"native_id":           request.Asset.Identity.NativeID,
		"state":               instance.status,
		"provider_request_id": instance.requestID,
	}
	if !instance.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	if deletionInProgressState(instance.status) {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence,
		}, nil
	}
	prepaid, renewalRequestID, err := a.isPrepaid(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence["renewal_request_id"] = renewalRequestID
	if prepaid {
		evidence["charge_type"] = "prepaid"
		evidence["next_operation"] = ensReleasePrepaidOperation
	} else {
		evidence["charge_type"] = "postpaid"
		evidence["next_operation"] = ensReleasePostOperation
	}
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *ENSAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, ENSInstanceNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !instance.exists {
		return contracts.ActionResult{
			ProviderRequestID: instance.requestID, Data: map[string]any{"phase": "absent"},
		}, nil
	}
	prepaid, _, err := a.isPrepaid(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation := ensReleasePostOperation
	chargeType := "postpaid"
	if prepaid {
		operation = ensReleasePrepaidOperation
		chargeType = "prepaid"
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   a.connectionID,
		Operation:      operation,
		Scope:          map[string]string{"region": a.region},
		Parameters:     map[string]any{"InstanceId": request.Asset.Identity.NativeID},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: map[string]any{
			"phase": "release", "charge_type": chargeType, "operation": operation,
		},
	}, nil
}

func (a *ENSAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	_ contracts.ActionResult,
) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: actionReadbackInterval, State: readback.State,
	}, nil
}

func (a *ENSAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, ENSInstanceNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	instance, err := a.readInstance(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	state := instance.status
	if !instance.exists {
		state = "absent"
	}
	return contracts.ReadbackResult{
		Exists: instance.exists, State: state, Data: instance.data,
	}, nil
}

func (a *ENSAction) readInstance(
	ctx context.Context,
	nativeID string,
) (ensInstance, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    ensDescribeOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"InstanceId": nativeID, "PageNumber": 1, "PageSize": 1,
		},
	})
	if isNotFound(err) {
		return ensInstance{}, nil
	}
	if err != nil {
		return ensInstance{}, err
	}
	items := valueAtPath(result.Data, "Instances.Instance")
	values, ok := items.([]any)
	if !ok || len(values) == 0 {
		return ensInstance{requestID: result.RequestID, data: result.Data}, nil
	}
	for _, item := range values {
		resource, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(resource["InstanceId"])) == strings.TrimSpace(nativeID) {
			return ensInstance{
				exists: true, status: stringValue(resource["Status"]),
				requestID: result.RequestID, data: result.Data,
			}, nil
		}
	}
	return ensInstance{}, fmt.Errorf(
		"Alibaba Cloud ENS readback returned instances but none matched %q",
		nativeID,
	)
}

func (a *ENSAction) isPrepaid(
	ctx context.Context,
	nativeID string,
) (bool, string, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    ensRenewalOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters:   map[string]any{"InstanceIds": nativeID},
	})
	if isNotFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	items := valueAtPath(result.Data, "InstanceRenewAttributes.InstanceRenewAttribute")
	values, _ := items.([]any)
	for _, item := range values {
		resource, ok := item.(map[string]any)
		if ok && strings.TrimSpace(stringValue(resource["InstanceId"])) ==
			strings.TrimSpace(nativeID) {
			return true, result.RequestID, nil
		}
	}
	return false, result.RequestID, nil
}

const (
	SSLCertificateNativeType = "ACS::SSLCertificatesService::Certificate"

	sslCertificateListOperation   = "AlibabaCloud.SSLCertificatesService.ListUserCertificateOrder"
	sslCertificateGetOperation    = "AlibabaCloud.SSLCertificatesService.GetUserCertificateDetail"
	sslCertificateDeleteOperation = "AlibabaCloud.SSLCertificatesService.DeleteUserCertificate"
)

type SSLCertificateAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type sslCertificate struct {
	exists    bool
	expired   bool
	uploaded  bool
	requestID string
	data      map[string]any
}

func NewSSLCertificateHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*SSLCertificateAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud SSL certificate action requires connection ID and region")
	}
	return &SSLCertificateAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

func (a *SSLCertificateAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, SSLCertificateNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	certificate, err := a.readCertificate(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"native_id":           request.Asset.Identity.NativeID,
		"expired":             certificate.expired,
		"uploaded":            certificate.uploaded,
		"provider_request_id": certificate.requestID,
	}
	if !certificate.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	revoked := false
	if !certificate.expired && !certificate.uploaded {
		var revokedRequestID string
		revoked, revokedRequestID, err = a.isRevoked(ctx, request.Asset.Identity.NativeID)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		evidence["revoked_query_request_id"] = revokedRequestID
	}
	evidence["revoked"] = revoked
	if !certificate.expired && !certificate.uploaded && !revoked {
		return contracts.PreflightResult{
			Allowed:  false,
			Reason:   "only expired, revoked, or manually uploaded certificates can be deleted through the product API",
			Evidence: evidence,
		}, nil
	}
	evidence["next_operation"] = sslCertificateDeleteOperation
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *SSLCertificateAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, SSLCertificateNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	certID, err := strconv.ParseInt(strings.TrimSpace(request.Asset.Identity.NativeID), 10, 64)
	if err != nil {
		return contracts.ActionResult{}, fmt.Errorf(
			"Alibaba Cloud SSL certificate ID %q is not numeric",
			request.Asset.Identity.NativeID,
		)
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID:   a.connectionID,
		Operation:      sslCertificateDeleteOperation,
		Scope:          map[string]string{"region": a.region},
		Parameters:     map[string]any{"CertId": certID},
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: map[string]any{"phase": "delete"},
	}, nil
}

func (a *SSLCertificateAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	_ contracts.ActionResult,
) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists {
		return contracts.WaitResult{Done: true, State: "absent"}, nil
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: actionReadbackInterval, State: readback.State,
	}, nil
}

func (a *SSLCertificateAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, SSLCertificateNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	certificate, err := a.readCertificate(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	state := "present"
	if !certificate.exists {
		state = "absent"
	}
	return contracts.ReadbackResult{
		Exists: certificate.exists, State: state, Data: certificate.data,
	}, nil
}

func (a *SSLCertificateAction) readCertificate(
	ctx context.Context,
	nativeID string,
) (sslCertificate, error) {
	certID, err := strconv.ParseInt(strings.TrimSpace(nativeID), 10, 64)
	if err != nil {
		return sslCertificate{}, fmt.Errorf(
			"Alibaba Cloud SSL certificate ID %q is not numeric",
			nativeID,
		)
	}
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    sslCertificateGetOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters:   map[string]any{"CertId": certID},
	})
	if isNotFound(err) {
		return sslCertificate{}, nil
	}
	if err != nil {
		return sslCertificate{}, err
	}
	got := strings.TrimSpace(stringValue(result.Data["Id"]))
	if got == "" {
		return sslCertificate{requestID: result.RequestID, data: result.Data}, nil
	}
	if got != strings.TrimSpace(nativeID) {
		return sslCertificate{}, fmt.Errorf(
			"Alibaba Cloud SSL certificate readback returned ID %q, want %q",
			got,
			nativeID,
		)
	}
	buyInAliyun, hasBuyInAliyun := result.Data["BuyInAliyun"]
	return sslCertificate{
		exists: true, expired: truthy(result.Data["Expired"]),
		uploaded:  hasBuyInAliyun && !truthy(buyInAliyun),
		requestID: result.RequestID, data: result.Data,
	}, nil
}

func (a *SSLCertificateAction) isRevoked(
	ctx context.Context,
	nativeID string,
) (bool, string, error) {
	const pageSize = 100
	lastRequestID := ""
	for page := 1; ; page++ {
		result, err := a.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: a.connectionID,
			Operation:    sslCertificateListOperation,
			Scope:        map[string]string{"region": a.region},
			Parameters: map[string]any{
				"Status": "REVOKED", "CurrentPage": page, "ShowSize": pageSize,
			},
		})
		if err != nil {
			return false, lastRequestID, err
		}
		lastRequestID = result.RequestID
		items, _ := result.Data["CertificateOrderList"].([]any)
		for _, item := range items {
			resource, ok := item.(map[string]any)
			if ok && strings.TrimSpace(stringValue(resource["CertificateId"])) ==
				strings.TrimSpace(nativeID) {
				return true, lastRequestID, nil
			}
		}
		total, totalOK := integerValue(result.Data["TotalCount"])
		if len(items) < pageSize || (totalOK && page*pageSize >= total) {
			return false, lastRequestID, nil
		}
	}
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	case int:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return false
	}
}
