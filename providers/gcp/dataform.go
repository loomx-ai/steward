package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	dataformRepositoryType = "dataform.googleapis.com/Repository"
	dataformInvocationType = "dataform.googleapis.com/WorkflowInvocation"
	dataformProof          = "_dataform_configuration"
)

func isDataform(kind string) bool { return strings.HasPrefix(kind, "dataform.googleapis.com/") }

// Bind native configuration before redaction. Output-only execution progress and
// scheduled history may advance while cancellation and child deletion complete.
// ReleaseConfig exposes no creation ID; its configuration is the strongest
// native comparison available, together with the repository creation time.
func dataformConfiguration(kind string, raw map[string]any) string {
	fields := map[string][]string{
		"Folder":             {"createTime", "displayName", "creatorIamPrincipal", "containingFolder", "teamFolderName"},
		"TeamFolder":         {"createTime", "displayName", "creatorIamPrincipal"},
		"Repository":         {"name", "createTime", "displayName", "labels", "containingFolder", "teamFolderName", "gitRemoteSettings", "npmrcEnvironmentVariablesSecretVersion", "kmsKeyName", "workspaceCompilationOverrides", "serviceAccount"},
		"Workspace":          {"name", "createTime", "disableMoves", "privateResourceMetadata"},
		"ReleaseConfig":      {"name", "gitCommitish", "disabled", "cronSchedule", "codeCompilationConfig", "timeZone"},
		"WorkflowConfig":     {"name", "createTime", "releaseConfig", "invocationConfig", "disabled", "cronSchedule", "timeZone"},
		"WorkflowInvocation": {"name", "compilationResult", "workflowConfig", "invocationConfig", "resolvedCompilationResult", "pipelineConfig", "privateResourceMetadata"},
		"CompilationResult":  {"name", "createTime", "codeCompilationConfig", "gitCommitish", "workspace", "releaseConfig", "resolvedGitCommitSha"},
	}
	value := map[string]any{}
	for _, field := range fields[last(kind)] {
		if raw[field] != nil {
			value[field] = raw[field]
		}
	}
	// Every reader validates the canonical name separately. Project ID/number
	// aliases in native response names are not changes to the configuration.
	delete(value, "name")
	if kind == dataformRepositoryType && object(raw["gitRemoteSettings"]) != nil {
		remote := map[string]any{}
		for _, field := range []string{"url", "defaultBranch", "authenticationTokenSecretVersion", "sshAuthenticationConfig", "gitRepositoryLink"} {
			if value := object(raw["gitRemoteSettings"])[field]; value != nil {
				remote[field] = value
			}
		}
		value["gitRemoteSettings"] = remote
	}
	if kind == dataformInvocationType {
		value["startTime"] = object(raw["invocationTiming"])["startTime"]
	}
	payload, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func dataformSameResource(kind string, planned, live map[string]any) error {
	if !isDataform(kind) {
		return nil
	}
	expected := text(planned[dataformProof])
	if expected == "" {
		expected = dataformConfiguration(kind, planned)
	}
	if expected != dataformConfiguration(kind, live) {
		return groupDenied("dataform_configuration_changed")
	}
	return nil
}

func (c *client) verifyDataformParent(ctx context.Context, target productTarget) error {
	if target.ParentType != dataformRepositoryType {
		return nil
	}
	if target.ParentConfiguration == "" {
		return groupDenied("dataform_parent_proof_missing")
	}
	kind, _ := findType(target.ParentType)
	endpoint, err := c.resourceURL(kind, target.ParentID)
	if err != nil {
		return err
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	if c.canonicalName("//dataform.googleapis.com/"+text(live["name"])) != target.ParentID || dataformConfiguration(target.ParentType, live) != target.ParentConfiguration {
		return groupDenied("dataform_parent_changed")
	}
	proofs, err := c.dataformContainers(ctx, target.ParentType, target.ParentID, live)
	if err != nil {
		return err
	}
	if dataformContainersHash(proofs) != target.ParentContainerChain {
		return groupDenied("dataform_container_chain_changed")
	}
	return nil
}

func (a *action) dataformPreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	if !isDataform(a.kind.NativeType) {
		return nil
	}
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || request.Asset.Identity.NativeType != a.kind.NativeType || request.Asset.Identity.Provider != asset.ProviderGCP || a.client.canonicalName("//dataform.googleapis.com/"+text(live["name"])) != request.Asset.Identity.NativeID {
		return groupDenied("dataform_identity_changed")
	}
	if text(request.Asset.Normalized[dataformProof]) == "" {
		return groupDenied("dataform_configuration_proof_missing")
	}
	if err := dataformSameResource(a.kind.NativeType, request.Asset.Normalized, live); err != nil {
		return err
	}
	if a.kind.NativeType == dataformRepositoryType || a.kind.NativeType == dataformFolderType {
		if err := a.verifyDataformContainer(ctx, request, live); err != nil {
			return err
		}
	} else if a.kind.NativeType != dataformTeamFolderType {
		parent := request.Asset.Identity.NativeID[:strings.LastIndex(request.Asset.Identity.NativeID, "/")]
		parent = parent[:strings.LastIndex(parent, "/")]
		if err := a.client.verifyDataformParent(ctx, productTarget{ParentType: dataformRepositoryType, ParentID: parent, ParentConfiguration: text(request.Asset.Normalized["_dataform_parent_configuration"]), ParentContainerChain: text(request.Asset.Normalized[dataformContainerChain])}); err != nil {
			return contracts.DependencyReadError(err)
		}
	}
	if a.kind.NativeType == dataformInvocationType {
		switch text(live["state"]) {
		case "RUNNING", "CANCELING", "SUCCEEDED", "FAILED", "CANCELLED":
		default:
			return groupDenied("dataform_workflow_state_unknown")
		}
	}
	return nil
}

func dataformPhase(request contracts.ActionRequest, phase string) contracts.ActionResult {
	return contracts.ActionResult{Data: map[string]any{"phase": phase, "resource": request.Asset.Identity.NativeID, "configuration": request.Asset.Normalized[dataformProof]}, RetryAfter: 2 * time.Second}
}

func (a *action) prepareDataformInvocation(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := a.dataformPreflight(ctx, request, live); err != nil {
		return contracts.ActionResult{}, err
	}
	switch text(live["state"]) {
	case "RUNNING":
		metadata, err := providerData()
		if err != nil {
			return contracts.ActionResult{}, err
		}
		operation, ok := metadata.catalog.Operation("dataform.projects.locations.repositories.workflowInvocations.cancel")
		if !ok {
			return contracts.ActionResult{}, fmt.Errorf("missing Dataform cancellation operation")
		}
		bound, err := catalog.BindREST(operation, map[string]any{"name": text(live["name"]), "body": map[string]any{}})
		if err != nil {
			return contracts.ActionResult{}, err
		}
		response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if err := operationError(response.Data, response.RequestID); err != nil {
			return contracts.ActionResult{}, err
		}
		result := dataformPhase(request, "dataform_cancel")
		result.ProviderRequestID = response.RequestID
		return result, nil
	case "CANCELING":
		return dataformPhase(request, "dataform_cancel"), nil
	default:
		result, err := a.delete(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		next := dataformPhase(request, "dataform_delete")
		next.ProviderRequestID = result.ProviderRequestID
		return next, nil
	}
}

func (a *action) waitDataformInvocation(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	phase := text(result.Data["phase"])
	if (phase != "dataform_cancel" && phase != "dataform_delete") || text(result.Data["resource"]) != request.Asset.Identity.NativeID || text(result.Data["configuration"]) == "" || text(result.Data["configuration"]) != text(request.Asset.Normalized[dataformProof]) || result.ProviderOperationID != "" {
		return contracts.WaitResult{}, groupDenied("invalid_dataform_action_phase")
	}
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !check.Allowed {
		return contracts.WaitResult{}, groupDenied(check.Reason)
	}
	if check.Absent {
		return contracts.WaitResult{Done: true}, nil
	}
	if phase == "dataform_delete" {
		return contracts.WaitResult{State: phase, RetryAfter: 2 * time.Second}, nil
	}
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if isNotFound(err) {
		return contracts.WaitResult{Done: true}, nil
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.dataformPreflight(ctx, request, live); err != nil {
		return contracts.WaitResult{}, err
	}
	if state := text(live["state"]); state == "RUNNING" || state == "CANCELING" {
		return contracts.WaitResult{State: "dataform_cancel", RetryAfter: 2 * time.Second}, nil
	}
	next, err := a.prepareDataformInvocation(ctx, request)
	return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, err
}
