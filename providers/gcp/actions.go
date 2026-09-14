package gcp

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Connections use gcp; accept the historical google-cloud identity spelling too.
func gcpPartition(partition string) bool { return partition == "gcp" || partition == "google-cloud" }

type action struct {
	identity         asset.Identity
	client           *client
	kind             resourceType
	endpoint         string
	deleteOperation  catalog.Operation
	deleteParameters map[string]any
}

func (r *Runtime) ResolveAction(ctx context.Context, id asset.ConnectionID, value asset.Asset) (contracts.ActionDriver, error) {
	if (value.Identity.NativeType == storagePoolType || value.Identity.NativeType == batchJobType || isDataproc(value.Identity.NativeType) || isDiscovery(value.Identity.NativeType) || isTPU(value.Identity.NativeType) || isFusion(value.Identity.NativeType) || isMetricsScope(value.Identity.NativeType) || isInfra(value.Identity.NativeType) || isFirewall(value.Identity.NativeType) || isIdentityGroup(value.Identity.NativeType)) && (id == "" || id != value.Identity.ConnectionID) {
		return nil, groupDenied("native_connection_changed")
	}
	kind, ok := findType(value.Identity.NativeType)
	if !ok || value.Identity.Provider != asset.ProviderGCP || len(kind.DeleteOperations) == 0 {
		return nil, fmt.Errorf("GCP resource has no action driver")
	}
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	endpoint, err := c.resourceURL(kind, value.Identity.NativeID)
	if err != nil {
		return nil, err
	}
	operation, parameters, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	return &action{identity: value.Identity, client: c, kind: kind, endpoint: endpoint, deleteOperation: operation, deleteParameters: parameters}, nil
}
func (a *action) DeletionCheckTimeout() time.Duration {
	if a.kind.NativeType == discoveryDataStoreType || a.kind.NativeType == discoveryCollectionType {
		return 7 * 24 * time.Hour
	}
	if a.isGKE() {
		return 2 * time.Hour // Native node draining may itself wait for one hour.
	}
	return time.Hour
}

func (a *action) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if a.kind.NativeType == storagePoolType {
		return a.storagePoolPreflight(ctx, request)
	}
	if request.Action != "delete" {
		return contracts.PreflightResult{Reason: "unsupported_action"}, nil
	}
	if isIdentityGroup(a.kind.NativeType) {
		return a.identityPreflight(ctx, request)
	}
	if isFirewall(a.kind.NativeType) {
		return a.firewallPreflight(ctx, request)
	}
	if a.kind.NativeType == infraGroup {
		return a.infraGroupPreflight(ctx, request)
	}
	if isInfra(a.kind.NativeType) {
		return a.infraPreflight(ctx, request)
	}
	if a.kind.NativeType == monitoredProjectType {
		return a.metricsPreflight(ctx, request)
	}
	if isFusion(a.kind.NativeType) {
		return a.fusionPreflight(ctx, request)
	}
	if isTPU(a.kind.NativeType) {
		return a.tpuPreflight(ctx, request)
	}
	if err := a.discoveryActionIdentity(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.dataprocActionIdentity(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	data, err := a.readResource(ctx)
	if isNotFound(err) {
		if a.isGKE() {
			read, err := a.gkeReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"gke_absent": true}}, err
		}
		if a.kind.NativeType == managerType {
			read, err := a.managedGroupReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"manager_absent": true}}, err
		}
		if a.kind.NativeType == dataprocClusterType {
			read, err := a.dataprocReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"dataproc_observing": true}}, err
		}
		if isDiscovery(a.kind.NativeType) {
			read, err := a.discoveryReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"service_parent_absent": true}}, err
		}
		if HasServiceCascade(a.kind.NativeType) {
			read, err := a.serviceCascadeReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"service_parent_absent": true}}, err
		}
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.discoveryPreflight(ctx, request, data, true); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.dataformPreflight(ctx, request, data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.dataprocPreflight(request, data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if resourceSoftDeleted(a.kind.NativeType, data) {
		return contracts.PreflightResult{Allowed: true, Absent: true, Evidence: map[string]any{"state": "soft_deleted"}}, nil
	}
	for _, field := range []string{"uid", "uniqueId", "createTime", "creationTime"} {
		if original := text(request.Asset.Normalized[field]); original != "" && original != text(data[field]) {
			return contracts.PreflightResult{Reason: "resource_identity_changed"}, nil
		}
	}
	if strings.HasPrefix(a.kind.NativeType, "compute.googleapis.com/") && text(request.Asset.Normalized["id"]) != "" && text(request.Asset.Normalized["id"]) != text(data["id"]) {
		return contracts.PreflightResult{Reason: "resource_identity_changed"}, nil
	}
	if strings.HasPrefix(a.kind.NativeType, "compute.googleapis.com/") && a.kind.NativeType != managerType {
		if owned, err := a.client.dataprocComputeHasCluster(ctx, a.kind.NativeType, data); err != nil || owned {
			return contracts.PreflightResult{Reason: "dataproc_compute_requires_cluster_cleanup"}, err
		}
	}
	if protectedComputeLabels(data) {
		return contracts.PreflightResult{Reason: "protected_labels"}, nil
	}
	if reason := protectionReason(a.kind.NativeType, data); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	if a.kind.NativeType == "managedkafka.googleapis.com/Topic" && last(text(data["name"])) == "__remote_log_metadata" {
		return contracts.PreflightResult{Reason: "internal_topic_requires_cluster_cleanup"}, nil
	}
	if a.kind.NativeType == "gkehub.googleapis.com/Membership" && len(object(object(data["endpoint"])["gkeCluster"])) == 0 {
		return contracts.PreflightResult{Reason: "membership_requires_native_cluster_unregister"}, nil
	}
	if a.kind.NativeType == dataprocClusterType {
		if err := a.dataprocClusterPreflight(ctx, request, data); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	if err := a.serviceCascadePreflight(ctx, request, data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if a.isGKE() {
		if reason, err := a.plannedGKE(ctx, request, data); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
		if a.kind.NativeType == clusterType {
			if err := a.gkeNetworkPreflight(ctx, request, data); err != nil {
				return contracts.PreflightResult{}, err
			}
		}
	}
	if a.kind.NativeType == instanceType {
		if reason, err := a.managedVMPreflight(ctx, request, data); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
		if _, reason, err := a.plannedDisks(request, data); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
	}
	if a.kind.NativeType == managerType {
		if _, reason, err := a.plannedGroup(ctx, request, data); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
	}
	if a.kind.NativeType == instanceGroupType {
		if reason, err := a.unmanagedGroupPreflight(ctx, request); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	for _, compiled := range metadata.bundle.Specs {
		if compiled.ResourceKind.NativeType != a.kind.NativeType {
			continue
		}
		for _, condition := range compiled.Definition.Actions[request.Action].Preconditions {
			if !slices.Contains(condition.AllowedValues, fmt.Sprint(productValue(data, condition.Path))) {
				return contracts.PreflightResult{Reason: condition.Reason}, nil
			}
		}
	}
	if strings.HasPrefix(a.kind.NativeType, "cloudkms.googleapis.com/") {
		if reason, err := a.kmsPreflight(ctx, data); err != nil || reason != "" {
			return contracts.PreflightResult{Reason: reason}, err
		}
	}
	if a.kind.NativeType == "storage.googleapis.com/Bucket" {
		// A bucket's globally unique name has no project component.
		number := fmt.Sprint(data["projectNumber"])
		if number != a.client.number {
			return contracts.PreflightResult{Reason: "bucket_project_not_verified"}, nil
		}
		objects, err := a.client.request(ctx, "GET", a.endpoint+"/o", url.Values{"maxResults": {"1"}, "versions": {"true"}})
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if len(array(objects["items"])) > 0 {
			return contracts.PreflightResult{Reason: "bucket_not_empty"}, nil
		}
	}
	if a.kind.NativeType == dataprocClusterType {
		return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"dataproc_observing": object(data["status"])["state"] == "DELETING"}}, nil
	}
	if a.kind.NativeType == batchJobType {
		return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"batch_deleting": object(data["status"])["state"] == "DELETION_IN_PROGRESS"}}, nil
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *action) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: check.Reason, Message: contracts.SafeProviderValidationMessage}}
	}
	if check.Absent {
		return contracts.ActionResult{}, nil
	}
	if isIdentityGroup(a.kind.NativeType) {
		return a.executeIdentityGroup(ctx, request)
	}
	if isFirewall(a.kind.NativeType) {
		return a.executeFirewall(ctx, request)
	}
	if a.kind.NativeType == infraGroup {
		return a.executeInfraGroup(ctx, request, text(check.Evidence["infra_stage"]))
	}
	if isInfra(a.kind.NativeType) {
		return a.executeInfra(ctx, request, text(check.Evidence["infra_stage"]))
	}
	if a.kind.NativeType == monitoredProjectType {
		return a.executeMetricsScope(ctx, request)
	}
	if a.kind.NativeType == dataprocClusterType && check.Evidence["dataproc_observing"] == true {
		return contracts.ActionResult{Data: dataprocClusterPhase(request, ""), RetryAfter: 2 * time.Second}, nil
	}
	if check.Evidence["manager_absent"] == true || check.Evidence["gke_absent"] == true || check.Evidence["service_parent_absent"] == true {
		if a.kind.NativeType == batchJobType {
			return contracts.ActionResult{Data: batchPhase(request, ""), RetryAfter: 2 * time.Second}, nil
		}
		if a.kind.NativeType == clusterType {
			return gkePhase("gke_delete"), nil
		}
		return contracts.ActionResult{RetryAfter: 2 * time.Second}, nil
	}
	if isFusion(a.kind.NativeType) {
		return a.executeFusion(ctx, request, text(check.Evidence["datafusion_stage"]))
	}
	if isTPU(a.kind.NativeType) {
		return a.prepareTPU(ctx, request)
	}
	if a.kind.NativeType == managerType {
		return a.prepareManagedGroup(ctx, request)
	}
	if a.kind.NativeType == instanceType {
		return a.prepareInstance(ctx, request)
	}
	if a.kind.NativeType == clusterType {
		return a.prepareGKENetwork(ctx, request)
	}
	if a.kind.NativeType == dataformInvocationType {
		return a.prepareDataformInvocation(ctx, request)
	}
	if a.kind.NativeType == dataprocJobType {
		return a.prepareDataprocJob(ctx, request)
	}
	if a.kind.NativeType == batchJobType && check.Evidence["batch_deleting"] == true {
		return contracts.ActionResult{Data: batchPhase(request, ""), RetryAfter: 2 * time.Second}, nil
	}
	return a.delete(ctx, request)
}

func (a *action) delete(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	parameters := map[string]any{}
	for key, value := range a.deleteParameters {
		parameters[key] = value
	}
	if a.deleteOperation.Call == nil {
		return contracts.ActionResult{}, fmt.Errorf("GCP deletion has no catalog operation")
	}
	if parameter := serviceCascadeRules[a.kind.NativeType].forceParameter; parameter != "" {
		// Execute has just verified the complete live child set against the
		// reviewed plan. Only documented native cascade switches are enabled.
		parameters[parameter] = true
	}
	if a.kind.NativeType == dataprocClusterType {
		parameters["clusterUuid"] = request.Asset.Normalized["clusterUuid"]
	}
	if a.kind.NativeType == dataprocTemplateType {
		parameters["version"] = request.Asset.Normalized["version"]
		if parameters["version"] == nil {
			parameters["version"] = 0
		}
	}
	if property := object(object(a.deleteOperation.InputSchema["properties"])["etag"]); property != nil {
		if etag := text(request.Asset.Normalized["etag"]); etag != "" {
			parameters["etag"] = etag
		}
	}
	if token := a.deleteOperation.Call.IdempotencyParameter; token != "" && request.IdempotencyKey != "" {
		parameters[token] = googleRequestID(request.IdempotencyKey)
	}
	if a.kind.NativeType == "backupdr.googleapis.com/DataSource" {
		body := map[string]any{}
		if request.IdempotencyKey != "" {
			body["requestId"] = googleRequestID(request.IdempotencyKey)
		}
		parameters["body"] = body
	}
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := response.Data
	if failure := operationError(data, response.RequestID); failure != nil {
		return contracts.ActionResult{}, failure
	}
	operation, err := a.operationURL(data)
	if a.kind.NativeType == storagePoolType {
		operation, err = a.storagePoolOperation(data, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: map[string]any{"operation": operation, "storage_pool_receipt": storagePoolReceipt(request, operation)}, RetryAfter: 2 * time.Second}, nil
	}
	if isDiscovery(a.kind.NativeType) {
		operation, err = a.discoveryOperation(data, response.RequestID)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: discoveryPhase(request, operation), RetryAfter: 2 * time.Second}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if a.kind.NativeType == dataprocClusterType {
		operation, err = a.dataprocOperation(data, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: dataprocClusterPhase(request, operation), RetryAfter: 2 * time.Second}, nil
	}
	if a.kind.NativeType == batchJobType {
		operation, err = a.batchOperation(data, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: batchPhase(request, operation), RetryAfter: 2 * time.Second}, nil
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: map[string]any{"operation": operation}, RetryAfter: 2 * time.Second}, nil
}
func (a *action) operationURL(data map[string]any) (string, error) {
	if a.isGKE() {
		return a.gkeOperationURL(data)
	}
	nativeType := a.kind.NativeType
	if strings.HasPrefix(nativeType, "compute.googleapis.com/") || nativeType == "sqladmin.googleapis.com/Instance" {
		name := text(data["name"])
		if !segmentPattern.MatchString(name) || name == "." || name == ".." {
			return "", fmt.Errorf("Google API omitted a valid deletion operation")
		}
		parts := strings.Split(a.endpoint, "/")
		return strings.Join(parts[:len(parts)-2], "/") + "/operations/" + name, nil
	}
	if a.regionalOperation() {
		name := text(data["name"])
		if name == "" && data["done"] == true {
			return "", nil
		}
		parts := strings.Split(name, "/")
		if len(parts) < 4 || parts[0] != "projects" || (parts[1] != a.client.project && parts[1] != a.client.number) || parts[len(parts)-2] != "operations" {
			return "", fmt.Errorf("Google API omitted a valid deletion operation")
		}
		for _, part := range parts {
			if !segmentPattern.MatchString(part) || part == "." || part == ".." {
				return "", fmt.Errorf("invalid Google deletion operation")
			}
		}
		endpoint, _ := url.Parse(a.endpoint)
		index := strings.Index(endpoint.Path, "/projects/")
		if index < 0 {
			return "", fmt.Errorf("Google deletion target has no project scope")
		}
		resourceName := a.client.canonicalName("//" + endpoint.Host + endpoint.Path[index:])
		operationParent := a.client.canonicalName("//" + endpoint.Host + "/" + strings.Join(parts[:len(parts)-2], "/"))
		if resourceName != operationParent && !strings.HasPrefix(resourceName, operationParent+"/") {
			return "", fmt.Errorf("Google deletion operation belongs to another resource scope")
		}
		metadata, err := providerData()
		if err != nil {
			return "", err
		}
		for _, operation := range metadata.catalog.Operations {
			if operation.Call == nil || operation.Call.Product != a.deleteOperation.Call.Product || operation.Call.Version != a.deleteOperation.Call.Version || !strings.HasSuffix(operation.ID, ".operations.get") || len(operation.Call.RawPathParameters) != 1 {
				continue
			}
			bound, err := catalog.BindREST(operation, map[string]any{operation.Call.RawPathParameters[0]: name})
			if err == nil {
				return bound.URL, nil
			}
		}
		return "", fmt.Errorf("GCP deletion operation has no catalog polling method")
	}
	return "", nil
}

func (a *action) regionalOperation() bool {
	_, hasDone := object(a.deleteOperation.OutputSchema["properties"])["done"]
	return hasDone
}
func operationError(data map[string]any, requestID string) error {
	detail := object(data["error"])
	if len(detail) == 0 {
		return nil
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProviderFailure, Code: "operation_failed", Message: contracts.SafeProviderValidationMessage, RequestID: requestID}}
}
func (a *action) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if a.kind.NativeType == storagePoolType {
		return a.storagePoolWait(ctx, request, result)
	}
	if isIdentityGroup(a.kind.NativeType) {
		return a.waitIdentityGroup(ctx, request, result)
	}
	if isFirewall(a.kind.NativeType) {
		return a.waitFirewall(ctx, request, result)
	}
	if a.kind.NativeType == infraGroup {
		return a.waitInfraGroup(ctx, request, result)
	}
	if isInfra(a.kind.NativeType) {
		return a.waitInfra(ctx, request, result)
	}
	if a.kind.NativeType == monitoredProjectType {
		return a.waitMetricsScope(ctx, request, result)
	}
	if isFusion(a.kind.NativeType) {
		return a.waitFusion(ctx, request, result)
	}
	if isTPU(a.kind.NativeType) {
		return a.waitTPU(ctx, request, result)
	}
	if isDiscovery(a.kind.NativeType) {
		return a.waitDiscovery(ctx, request, result)
	}
	if err := a.dataprocActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if a.kind.NativeType == dataprocClusterType {
		if len(result.Data) == 0 && result.ProviderOperationID == "" {
			read, err := a.dataprocReadback(ctx, request)
			return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
		}
		return a.waitDataprocCluster(ctx, request, result)
	}
	if a.kind.NativeType == dataprocJobType && len(result.Data) > 0 {
		return a.waitDataprocJob(ctx, request, result)
	}
	if a.kind.NativeType == batchJobType {
		// Execute may return an empty result when its initial read already proved
		// the reviewed job and all effects absent, or DELETE itself returned 404.
		if len(result.Data) == 0 && result.ProviderOperationID == "" {
			read, err := a.Readback(ctx, request)
			return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
		}
		return a.waitBatch(ctx, request, result)
	}
	if a.kind.NativeType == dataformInvocationType && text(result.Data["phase"]) != "" {
		return a.waitDataformInvocation(ctx, request, result)
	}
	if a.kind.NativeType == clusterType && text(result.Data["phase"]) != "" {
		return a.waitGKENetwork(ctx, request, result)
	}
	if a.kind.NativeType == managerType && text(result.Data["phase"]) != "" {
		return a.waitManagedGroup(ctx, request, result)
	}
	phase := text(result.Data["phase"])
	if phase != "" {
		if a.kind.NativeType != instanceType || (phase != "prepare_instance" && phase != "delete") {
			return contracts.WaitResult{}, fmt.Errorf("invalid GCP action phase")
		}
		result.ProviderOperationID = text(result.Data["operation"])
		if phase == "prepare_instance" && result.ProviderOperationID == "" {
			return contracts.WaitResult{}, fmt.Errorf("missing GCP preparation operation")
		}
	}
	wait, err := a.waitOperation(ctx, result.ProviderOperationID)
	if err != nil || !wait.Done {
		return wait, err
	}
	if phase == "prepare_instance" {
		applied, err := a.instancePreparationApplied(ctx, request, result)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if !applied {
			return contracts.WaitResult{State: "preparing_instance", RetryAfter: 2 * time.Second}, nil
		}
		// Continue one provider operation per job attempt. All intermediate state
		// is persisted by the executor, including after a worker restart.
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
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}

func (a *action) waitOperation(ctx context.Context, operationID string) (contracts.WaitResult, error) {
	if operationID != "" {
		// Rebuild the operation from its name and the validated resource endpoint.
		// A persisted waiter URL cannot redirect credentials to another project/API.
		endpoint, _ := url.Parse(operationID)
		if endpoint == nil {
			return contracts.WaitResult{}, fmt.Errorf("invalid GCP operation")
		}
		name := last(endpoint.Path)
		if a.regionalOperation() {
			name = strings.TrimPrefix(strings.TrimPrefix(endpoint.Path, "/"), strings.Split(strings.TrimPrefix(endpoint.Path, "/"), "/")[0]+"/")
		}
		expected, err := a.operationURL(map[string]any{"name": name})
		if err != nil || expected != operationID {
			return contracts.WaitResult{}, fmt.Errorf("GCP operation identity mismatch")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			data := response.Data
			if a.isGKE() {
				if _, err := a.gkeOperationURL(data); err != nil {
					return contracts.WaitResult{}, err
				}
				if text(data["name"]) != name {
					return contracts.WaitResult{}, fmt.Errorf("GKE returned another operation")
				}
				if text(data["statusMessage"]) != "" && text(data["status"]) == "DONE" {
					return contracts.WaitResult{}, groupDenied("gke_operation_failed")
				}
			}
			if failure := operationError(data, response.RequestID); failure != nil {
				return contracts.WaitResult{}, failure
			}
			done := data["done"] == true || text(data["status"]) == "DONE"
			if !done {
				return contracts.WaitResult{RetryAfter: 2 * time.Second, State: text(data["status"])}, nil
			}
		}
	}
	return contracts.WaitResult{Done: true}, nil
}
func (a *action) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if a.kind.NativeType == storagePoolType {
		return a.storagePoolReadback(ctx, request)
	}
	if isIdentityGroup(a.kind.NativeType) {
		return a.identityReadback(ctx, request)
	}
	if isFirewall(a.kind.NativeType) {
		return a.firewallReadback(ctx, request)
	}
	if a.kind.NativeType == infraGroup {
		return a.infraGroupReadback(ctx, request)
	}
	if isInfra(a.kind.NativeType) {
		return a.infraReadback(ctx, request)
	}
	if a.kind.NativeType == monitoredProjectType {
		return a.metricsReadback(ctx, request)
	}
	if isFusion(a.kind.NativeType) {
		return a.fusionReadback(ctx, request)
	}
	if isTPU(a.kind.NativeType) {
		return a.tpuReadback(ctx, request)
	}
	if err := a.discoveryActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.dataprocActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.kind.NativeType == dataprocClusterType {
		return a.dataprocReadback(ctx, request)
	}
	data, err := a.readResource(ctx)
	if isNotFound(err) {
		if a.isGKE() {
			return a.gkeReadback(ctx, request)
		}
		if a.kind.NativeType == managerType {
			return a.managedGroupReadback(ctx, request)
		}
		if isDiscovery(a.kind.NativeType) {
			return a.discoveryReadback(ctx, request)
		}
		if HasServiceCascade(a.kind.NativeType) {
			return a.serviceCascadeReadback(ctx, request)
		}
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.discoveryPreflight(ctx, request, data, false); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.dataformPreflight(ctx, request, data); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.dataprocPreflight(request, data); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.kind.NativeType == batchJobType {
		if err := a.batchJobIdentity(request, data); err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	if resourceSoftDeleted(a.kind.NativeType, data) {
		return contracts.ReadbackResult{Exists: false, State: "soft_deleted"}, nil
	}
	return contracts.ReadbackResult{Exists: true, State: resourceState(data)}, nil
}

func protectionReason(nativeType string, data map[string]any) string {
	if nativeType == monitoredProjectType {
		parts := strings.Split(text(data["name"]), "/")
		if len(parts) >= 6 && parts[len(parts)-1] == parts[len(parts)-3] {
			return "metrics_scope_self_protected"
		}
	}
	if nativeType != instanceType && (data["deletionProtection"] == true || object(data["settings"])["deletionProtectionEnabled"] == true) {
		return "deletion_protection_enabled"
	}
	if data["deleteProtectionState"] == "DELETE_PROTECTION_ENABLED" || data["enableDropProtection"] == true || data["deletionProtectionEnabled"] == true {
		return "deletion_protection_enabled"
	}
	if nativeType == "logging.googleapis.com/LogBucket" && (data["locked"] == true || data["name"] != nil && last(text(data["name"])) == "_Required") {
		return "log_bucket_retention_locked"
	}
	if nativeType == "backupdr.googleapis.com/Backup" {
		for _, field := range []string{"serviceLocks", "backupApplianceLocks"} {
			for _, lock := range array(data[field]) {
				end, err := time.Parse(time.RFC3339Nano, text(object(lock)["lockUntilTime"]))
				if err != nil || time.Now().Before(end) {
					return "backup_locked"
				}
			}
		}
		if value := text(data["enforcedRetentionEndTime"]); value != "" {
			end, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || time.Now().Before(end) {
				return "backup_retention_active"
			}
		}
	}
	return ""
}

func resourceSoftDeleted(nativeType string, data map[string]any) bool {
	return nativeType == "iam.googleapis.com/Role" && data["deleted"] == true || nativeType == "logging.googleapis.com/LogBucket" && data["lifecycleState"] == "DELETE_REQUESTED"
}
