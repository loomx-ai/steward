package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) identitySaved(value asset.Asset) ([]identityMemberProof, error) {
	name, err := identityName(value.Identity.NativeType, value.Identity.NativeID)
	if err != nil {
		return nil, err
	}
	data := value.Normalized
	if err := c.identityValidate(value.Identity.NativeType, name, data); err != nil {
		return nil, err
	}
	if data[identityScope] != c.identityParent || data[identityProof] != identityConfiguration(data) || len(text(data[identityParentProof])) != 64 || data[identitySnapshot] != identityManifest(data) {
		return nil, groupDenied("identity_group_review_changed")
	}
	if value.Identity.NativeType == identityGroupType && data[identityParentProof] != data[identityProof] {
		return nil, groupDenied("identity_group_parent_review_changed")
	}
	var proofs []identityMemberProof
	if json.Unmarshal([]byte(text(data[identityMembers])), &proofs) != nil || proofs == nil {
		return nil, groupDenied("identity_membership_review_invalid")
	}
	found := value.Identity.NativeType == identityGroupType
	for i, proof := range proofs {
		if _, err := identityName(identityMemberType, "//"+identityHost+"/"+proof.ID); err != nil {
			return nil, err
		}
		if identityGroupName(proof.ID) != identityGroupName(name) || len(proof.Proof) != 64 || i > 0 && proofs[i-1].ID >= proof.ID {
			return nil, groupDenied("identity_membership_review_invalid")
		}
		found = found || proof.ID == name && proof.Proof == data[identityProof]
	}
	if !found {
		return nil, groupDenied("identity_membership_review_missing")
	}
	return proofs, nil
}
func (a *action) identityAction(request contracts.ActionRequest) ([]identityMemberProof, error) {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || a.identity.ConnectionID == "" || len(request.Parameters) != 0 || len(request.PrerequisiteDeletions) != 0 {
		return nil, groupDenied("identity_group_action_changed")
	}
	endpoint, err := a.client.resourceURL(a.kind, a.identity.NativeID)
	if err != nil || endpoint != a.endpoint {
		return nil, groupDenied("identity_group_endpoint_changed")
	}
	proofs, err := a.client.identitySaved(request.Asset)
	if err != nil {
		return nil, err
	}
	if a.kind.NativeType == identityMemberType {
		if len(request.LifecycleImpacts) != 0 {
			return nil, groupDenied("identity_membership_impacts_invalid")
		}
		return proofs, nil
	}
	if len(proofs) != len(request.LifecycleImpacts) {
		return nil, groupDenied("identity_group_impacts_changed")
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return nil, err
	}
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, proof := range proofs {
		impact, ok := impacts[groupImpactKey{request.Asset.ID, "//" + identityHost + "/" + proof.ID}]
		child := impact.Asset
		if !ok || !impact.Delete || child.Identity.NativeType != identityMemberType || seen[child.ID] {
			return nil, groupDenied("identity_group_impact_invalid")
		}
		if _, err := a.client.identitySaved(child); err != nil {
			return nil, err
		}
		if child.Normalized[identityParentProof] != request.Asset.Normalized[identityProof] || child.Normalized[identityMembers] != request.Asset.Normalized[identityMembers] || child.Normalized[identityProof] != proof.Proof {
			return nil, groupDenied("identity_group_impact_review_changed")
		}
		seen[child.ID] = true
	}
	return proofs, nil
}
func identitySame(planned, live map[string]any) error {
	if planned[identitySnapshot] != live[identitySnapshot] || identityManifest(planned) != identityManifest(live) || identityConfiguration(planned) != identityConfiguration(live) {
		return groupDenied("identity_group_configuration_or_members_changed")
	}
	return nil
}
func (c *client) identityChildren(ctx context.Context, parent asset.Identity, planned map[string]any) ([]serviceChild, error) {
	if _, err := c.identitySaved(asset.Asset{Identity: parent, Normalized: planned}); err != nil {
		return nil, err
	}
	name, err := identityName(identityGroupType, parent.NativeID)
	if err != nil {
		return nil, err
	}
	view, err := c.identityGroupView(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := identitySame(planned, view.Group); err != nil {
		return nil, err
	}
	children := []serviceChild{}
	for name, data := range view.Members {
		children = append(children, serviceChild{kind: identityMemberType, id: "//" + identityHost + "/" + name, data: data})
	}
	slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return children, nil
}
func (a *action) identityPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if _, err := a.identityAction(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	name, _ := identityName(a.kind.NativeType, a.identity.NativeID)
	view, err := a.client.identityGroupView(ctx, identityGroupName(name))
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if identityGroupProtected(view.Group) {
		return contracts.PreflightResult{Reason: "identity_group_locked_or_protected"}, nil
	}
	if identityGroupDynamic(view.Group) && object(object(view.Group["dynamicGroupMetadata"])["status"])["status"] != "UP_TO_DATE" {
		return contracts.PreflightResult{Reason: "identity_dynamic_memberships_not_ready"}, nil
	}
	if a.kind.NativeType == identityMemberType {
		if identityGroupDynamic(view.Group) {
			return contracts.PreflightResult{Reason: "identity_dynamic_membership_managed"}, nil
		}
		if request.Asset.Normalized[identityParentProof] != view.Group[identityProof] {
			return contracts.PreflightResult{}, groupDenied("identity_membership_parent_changed")
		}
		live := view.Members[name]
		if live == nil {
			// A list alone may be filtered; require the exact unique membership GET.
			_, err := a.client.identityRead(ctx, identityMemberType, name)
			if !isNotFound(err) {
				if err == nil {
					err = groupDenied("identity_membership_list_incomplete")
				}
				return contracts.PreflightResult{}, err
			}
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if request.Asset.Normalized[identityProof] != live[identityProof] {
			return contracts.PreflightResult{}, groupDenied("identity_membership_configuration_changed")
		}
	} else if err := identitySame(request.Asset.Normalized, view.Group); err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func identityReceiptBinding(request contracts.ActionRequest) string {
	return firewallDigest([]any{request.Asset.ID, request.Asset.Identity, request.Asset.Normalized[identitySnapshot], request.IdempotencyKey})
}
func identityOperation(data map[string]any) (bool, error) {
	if data == nil {
		return false, groupDenied("identity_group_operation_missing")
	}
	done := false
	if value, present := data["done"]; present {
		var ok bool
		done, ok = value.(bool)
		if !ok {
			return false, groupDenied("identity_group_operation_invalid")
		}
	}
	if value, present := data["error"]; present && value != nil {
		return false, groupDenied("identity_group_operation_failed")
	}
	if value, present := data["response"]; present {
		if _, ok := value.(map[string]any); !ok || !done {
			return false, groupDenied("identity_group_operation_invalid")
		}
	}
	if value, present := data["name"]; present {
		name, ok := value.(string)
		if !ok || len(name) > 1024 {
			return false, groupDenied("identity_group_operation_name_invalid")
		}
		if name != "" {
			parts := strings.Split(name, "/")
			if len(parts) < 2 || parts[len(parts)-2] != "operations" {
				return false, groupDenied("identity_group_operation_name_invalid")
			}
			for _, part := range parts {
				if !identitySegment.MatchString(part) || part == "-" {
					return false, groupDenied("identity_group_operation_name_invalid")
				}
			}
		}
	}
	return done, nil
}
func (a *action) executeIdentityGroup(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	// Execute has just repeated preflight. DELETE has no native etag/request ID.
	bound, err := catalog.BindREST(a.deleteOperation, a.deleteParameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	if _, err := identityOperation(result.Data); err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: result.RequestID, ProviderOperationID: text(result.Data["name"]), Data: map[string]any{"phase": "identity_group_delete", "binding": identityReceiptBinding(request), "operation": result.Data}, RetryAfter: 2 * time.Second}, nil
}
func identityReceipt(request contracts.ActionRequest, result *contracts.ActionResult) (bool, error) {
	if result == nil || len(result.Data) == 0 {
		return false, nil
	}
	if result.Data["phase"] != "identity_group_delete" || result.Data["binding"] != identityReceiptBinding(request) {
		return false, groupDenied("identity_group_receipt_changed")
	}
	operation, ok := result.Data["operation"].(map[string]any)
	if !ok || result.ProviderOperationID != text(operation["name"]) {
		return false, groupDenied("identity_group_receipt_invalid")
	}
	return identityOperation(operation)
}
func identityPermissionDenied(err error) bool {
	var call *contracts.ProviderCallError
	var status googleResponseStatus
	return errors.As(err, &call) && errors.As(err, &status) && status == 403 && call.Provider.Category == execution.ErrorPermissionDenied && (call.Provider.Code == "403" || call.Provider.Code == "PERMISSION_DENIED")
}
func (a *action) identityReadback(ctx context.Context, request contracts.ActionRequest) (result contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	proofs, err := a.identityAction(request)
	if err != nil {
		return result, err
	}
	confirmed, err := identityReceipt(request, request.ExecutionResult)
	if err != nil {
		return result, err
	}
	name, _ := identityName(a.kind.NativeType, a.identity.NativeID)
	// Global unique IDs cannot be recreated. A successful native delete receipt
	// can resolve the API's ambiguous 403 only for this exact reviewed operation.
	// All readable survivors still block completion, even after done:true.
	observe := func(kind, name, proof string) (bool, error) {
		live, err := a.client.request(ctx, "GET", "https://"+identityHost+"/v1/"+name, nil)
		if isNotFound(err) {
			return false, nil
		}
		if identityPermissionDenied(err) && confirmed {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if err := a.client.identityComplete(ctx, kind, name, live); err != nil {
			return true, err
		}
		if identityConfiguration(live) != proof {
			return true, groupDenied("identity_group_readback_resource_changed")
		}
		return true, nil
	}
	for pass := 0; pass < 2; pass++ {
		exists, err := observe(a.kind.NativeType, name, text(request.Asset.Normalized[identityProof]))
		if err != nil {
			return result, err
		}
		if exists {
			return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
		}
		if a.kind.NativeType == identityGroupType {
			for _, proof := range proofs {
				exists, err := observe(identityMemberType, proof.ID, proof.Proof)
				if err != nil {
					return result, err
				}
				if exists {
					return contracts.ReadbackResult{Exists: true, State: "memberships_deleting"}, nil
				}
			}
		} else {
			// Independent unlink must preserve the same containing group. The group
			// DELETE route has its own receipt and verifies its reviewed native cascade.
			parent, err := a.client.identityRead(ctx, identityGroupType, identityGroupName(name))
			if err != nil {
				return result, err
			}
			if identityConfiguration(parent) != request.Asset.Normalized[identityParentProof] {
				return result, groupDenied("identity_membership_retained_parent_changed")
			}
		}
	}
	state := "absent"
	if confirmed {
		state = "delete_confirmed"
	}
	return contracts.ReadbackResult{Exists: false, State: state, Data: map[string]any{"native_delete_confirmed": confirmed}}, nil
}
func (a *action) waitIdentityGroup(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	read, err := a.identityReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second, Data: result.Data}, err
}
