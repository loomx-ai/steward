package gcp

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// The manifest is a read snapshot, not a native compare-and-swap token. Neither
// DELETE nor removeAssociation accepts the policy fingerprint as a precondition.
func (c *client) firewallSaved(value asset.Asset) ([]firewallMember, error) {
	kind, id, data := value.Identity.NativeType, value.Identity.NativeID, value.Normalized
	parent, name, err := c.firewallIdentityParts(kind, id)
	if err != nil {
		return nil, err
	}
	scope := "projects/" + c.project
	if firewallParentType(kind) == firewallPolicyType {
		scope = c.firewallParent
	}
	if data[firewallScope] != scope || text(data[firewallSnapshotKey]) == "" || data[firewallSnapshotKey] != firewallManifestDigest(data) || data[firewallProof] != firewallConfiguration(data, false) {
		return nil, groupDenied("firewall_review_changed")
	}
	if firewallParentType(kind) == firewallPolicyType && (len(text(data[firewallOwnerProof])) != 64 || !firewallContainerName(text(data[firewallOwnerName]))) {
		return nil, groupDenied("firewall_owner_review_missing")
	}
	if isFirewallPolicy(kind) {
		if kind == firewallPolicyType && data[firewallOwnerName] != data["parent"] {
			return nil, groupDenied("firewall_owner_name_changed")
		}
		if err := c.firewallPolicyIdentity(kind, id, data); err != nil {
			return nil, err
		}
		if data[firewallBaseProof] != firewallConfiguration(data, true) {
			return nil, groupDenied("firewall_base_review_changed")
		}
	} else {
		if data[firewallContainingPolicy] != parent || text(data["name"]) != name || !firewallNumericID(text(data["firewallPolicyId"])) || len(text(data[firewallParentProof])) != 64 || len(text(data[firewallParentFullProof])) != 64 || text(data[firewallTargetProof]) == "" {
			return nil, groupDenied("firewall_association_review_changed")
		}
		actual, err := c.firewallAssociationValue(kind, parent, map[string]any{"id": data["firewallPolicyId"]}, data)
		if err != nil || actual != id {
			return nil, groupDenied("firewall_association_review_identity_invalid")
		}
	}
	var members []firewallMember
	if json.Unmarshal([]byte(text(data[firewallMembersKey])), &members) != nil {
		return nil, groupDenied("firewall_members_review_invalid")
	}
	seen := map[string]bool{}
	for i, member := range members {
		actual, _, err := c.firewallIdentityParts(firewallChildType(firewallParentType(kind)), member.ID)
		if err != nil || actual != parent || seen[member.ID] || len(member.Proof) != 64 || member.TargetProof == "" || i > 0 && members[i-1].ID >= member.ID {
			return nil, groupDenied("firewall_member_review_invalid")
		}
		seen[member.ID] = true
	}
	if isFirewallPolicy(kind) {
		rows, err := c.firewallAssociationRows(firewallChildType(kind), id, data)
		if err != nil || len(rows) != len(members) {
			return nil, groupDenied("firewall_review_membership_changed")
		}
		for _, member := range members {
			if rows[member.ID] == nil || firewallConfiguration(rows[member.ID], false) != member.Proof {
				return nil, groupDenied("firewall_review_association_changed")
			}
		}
	} else {
		found := false
		for _, member := range members {
			found = found || member.ID == id && member.Proof == data[firewallProof] && member.TargetProof == data[firewallTargetProof]
		}
		if !found {
			return nil, groupDenied("firewall_association_missing_from_review")
		}
	}
	return members, nil
}

func firewallSame(planned, live map[string]any) error {
	if text(planned[firewallSnapshotKey]) == "" || planned[firewallSnapshotKey] != live[firewallSnapshotKey] || firewallManifestDigest(planned) != firewallManifestDigest(live) || firewallConfiguration(planned, false) != firewallConfiguration(live, false) {
		return groupDenied("firewall_snapshot_changed")
	}
	return nil
}

func (a *action) firewallActionIdentity(request contracts.ActionRequest) ([]firewallMember, error) {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || a.identity.ConnectionID == "" || len(request.LifecycleImpacts) != 0 || len(request.Parameters) != 0 {
		return nil, groupDenied("firewall_action_changed")
	}
	endpoint, err := a.client.resourceURL(a.kind, a.identity.NativeID)
	if err != nil || endpoint != a.endpoint {
		return nil, groupDenied("firewall_endpoint_changed")
	}
	members, err := a.client.firewallSaved(request.Asset)
	if err != nil {
		return nil, err
	}
	if !isFirewallPolicy(a.kind.NativeType) {
		if len(request.PrerequisiteDeletions) != 0 {
			return nil, groupDenied("firewall_association_prerequisites_invalid")
		}
		return members, nil
	}
	if len(members) != len(request.PrerequisiteDeletions) {
		return nil, groupDenied("firewall_prerequisites_changed")
	}
	byID := map[string]firewallMember{}
	for _, member := range members {
		byID[member.ID] = member
	}
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		child, identity := prerequisite.Asset, prerequisite.Asset.Identity
		member, found := byID[identity.NativeID]
		if !found || !prerequisite.Delete || prerequisite.ControllerID != request.Asset.ID || child.ID == "" || seen[child.ID] || identity.Provider != a.identity.Provider || identity.ConnectionID != a.identity.ConnectionID || identity.Partition != a.identity.Partition || identity.NativeType != firewallChildType(a.kind.NativeType) {
			return nil, groupDenied("firewall_prerequisite_identity_changed")
		}
		if _, err := a.client.firewallSaved(child); err != nil {
			return nil, err
		}
		if child.Normalized[firewallContainingPolicy] != a.identity.NativeID || child.Normalized[firewallParentProof] != request.Asset.Normalized[firewallBaseProof] || child.Normalized[firewallParentFullProof] != request.Asset.Normalized[firewallProof] || child.Normalized[firewallMembersKey] != request.Asset.Normalized[firewallMembersKey] || text(child.Normalized[firewallOwnerProof]) != text(request.Asset.Normalized[firewallOwnerProof]) || child.Normalized[firewallProof] != member.Proof || child.Normalized[firewallTargetProof] != member.TargetProof {
			return nil, groupDenied("firewall_prerequisite_snapshot_changed")
		}
		delete(byID, identity.NativeID)
		seen[child.ID] = true
	}
	return members, nil
}

func (c *client) firewallChildren(ctx context.Context, identity asset.Identity, planned map[string]any) ([]serviceChild, error) {
	if _, err := c.firewallSaved(asset.Asset{Identity: identity, Normalized: planned}); err != nil {
		return nil, err
	}
	live, err := c.firewallReadPolicy(ctx, identity.NativeType, identity.NativeID)
	if err != nil {
		return nil, err
	}
	rows, err := c.firewallSnapshot(ctx, identity.NativeType, identity.NativeID, live)
	if err != nil {
		return nil, err
	}
	if err := firewallSame(planned, live); err != nil {
		return nil, err
	}
	var children []serviceChild
	for id, data := range rows {
		children = append(children, serviceChild{kind: firewallChildType(identity.NativeType), id: id, data: data, direct: true})
	}
	slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return children, nil
}

// An independent target-side list exists for hierarchical associations. It has
// no pagination. Other policies on this target are preserved and are not cleanup
// effects of the selected policy. Network policy APIs offer no equivalent list.
func (c *client) firewallReverseAbsent(ctx context.Context, kind string, data map[string]any) error {
	proof, err := c.firewallTarget(ctx, kind, data)
	if err != nil {
		return err
	}
	if proof != data[firewallTargetProof] {
		return groupDenied("firewall_target_changed")
	}
	if firewallParentType(kind) != firewallPolicyType {
		return nil
	}
	target := text(data["attachmentTarget"])
	rows, err := c.firewallList(ctx, "compute.firewallPolicies.listAssociations", map[string]any{"targetResource": target, "includeInheritedPolicies": false}, "associations")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		key := text(row["firewallPolicyId"])
		if row["attachmentTarget"] != target || !firewallNumericID(key) || !firewallAssociationName(text(row["name"])) || seen[key] {
			return groupDenied("firewall_reverse_list_invalid")
		}
		seen[key] = true
		if key == data["firewallPolicyId"] {
			return groupDenied("firewall_reverse_association_still_exists")
		}
	}
	return nil
}

func (a *action) firewallObserve(ctx context.Context, request contracts.ActionRequest, empty bool) (contracts.ReadbackResult, error) {
	members, err := a.firewallActionIdentity(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	kind, planned := a.kind.NativeType, request.Asset.Normalized
	parent, _, _ := a.client.firewallIdentityParts(kind, a.identity.NativeID)
	base, full := text(planned[firewallParentProof]), text(planned[firewallParentFullProof])
	if isFirewallPolicy(kind) {
		base, full = text(planned[firewallBaseProof]), text(planned[firewallProof])
	}
	expected := map[string]firewallMember{}
	for _, member := range members {
		expected[member.ID] = member
	}
	var previous string
	var exists bool
	for pass := 0; pass < 2; pass++ {
		policy, err := a.client.firewallReadPolicy(ctx, firewallParentType(kind), parent)
		if err != nil && !isNotFound(err) {
			return contracts.ReadbackResult{}, err
		}
		rows := map[string]map[string]any{}
		if err == nil {
			if policy[firewallBaseProof] != base || text(policy[firewallOwnerProof]) != text(planned[firewallOwnerProof]) || policy[firewallScope] != planned[firewallScope] {
				return contracts.ReadbackResult{}, groupDenied("firewall_policy_configuration_changed")
			}
			rows, err = a.client.firewallSnapshot(ctx, firewallParentType(kind), parent, policy)
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			if len(rows) == len(expected) && policy[firewallProof] != full {
				return contracts.ReadbackResult{}, groupDenied("firewall_policy_fingerprint_changed")
			}
			for id, row := range rows {
				member, known := expected[id]
				if !known || row[firewallProof] != member.Proof || row[firewallTargetProof] != member.TargetProof {
					return contracts.ReadbackResult{}, groupDenied("firewall_live_membership_changed")
				}
			}
		} else if firewallParentType(kind) == firewallPolicyType {
			chain, err := a.client.firewallContainerChain(ctx, text(planned[firewallOwnerName]))
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			if chain != planned[firewallOwnerProof] {
				return contracts.ReadbackResult{}, groupDenied("firewall_deleted_policy_owner_changed")
			}
		}
		fingerprint := "absent"
		if policy != nil {
			fingerprint = text(policy[firewallSnapshotKey])
		}
		if pass > 0 && previous != fingerprint {
			return contracts.ReadbackResult{}, groupDenied("firewall_readback_changed")
		}
		previous = fingerprint
		targets := []asset.Asset{request.Asset}
		if isFirewallPolicy(kind) {
			targets = nil
			for _, child := range request.PrerequisiteDeletions {
				targets = append(targets, child.Asset)
			}
			exists = policy != nil
			if len(rows) != 0 && empty {
				return contracts.ReadbackResult{}, groupDenied("firewall_prerequisite_still_exists")
			}
		}
		for _, target := range targets {
			live, err := a.client.firewallAssociationGET(ctx, target.Identity.NativeType, target.Identity.NativeID)
			if err != nil && !isNotFound(err) {
				return contracts.ReadbackResult{}, err
			}
			inline := rows[target.Identity.NativeID]
			if err == nil {
				if inline == nil || firewallConfiguration(live, false) != target.Normalized[firewallProof] {
					return contracts.ReadbackResult{}, groupDenied("firewall_association_visibility_changed")
				}
				if isFirewallPolicy(kind) {
					return contracts.ReadbackResult{}, groupDenied("firewall_prerequisite_still_exists")
				}
				exists = true
			} else {
				if inline != nil {
					return contracts.ReadbackResult{}, groupDenied("firewall_association_visibility_changed")
				}
				if err := a.client.firewallReverseAbsent(ctx, target.Identity.NativeType, target.Normalized); err != nil {
					return contracts.ReadbackResult{}, err
				}
				if !isFirewallPolicy(kind) {
					exists = false
				}
			}
		}
	}
	return contracts.ReadbackResult{Exists: exists}, nil
}

func (a *action) firewallReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	return a.firewallObserve(ctx, request, true)
}

func (a *action) firewallPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.firewallReadback(ctx, request)
	if err == nil && read.Exists && protectedComputeLabels(request.Asset.Normalized) {
		return contracts.PreflightResult{Reason: "protected_labels"}, nil
	}
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func (a *action) firewallRequestID(request contracts.ActionRequest) string {
	return googleRequestID(request.IdempotencyKey + "/" + a.identity.NativeType + "/" + a.identity.NativeID + "/" + text(request.Asset.Normalized[firewallSnapshotKey]) + "/" + serviceReview(request))
}

func (a *action) firewallOperationURL(name string) (string, error) {
	if !segmentPattern.MatchString(name) || name == "." || name == ".." {
		return "", groupDenied("firewall_operation_name_invalid")
	}
	parent, _, err := a.client.firewallIdentityParts(a.kind.NativeType, a.identity.NativeID)
	if err != nil {
		return "", err
	}
	p := strings.Split(strings.TrimPrefix(parent, "//compute.googleapis.com/"), "/")
	return "https://compute.googleapis.com/compute/v1/" + strings.Join(p[:len(p)-2], "/") + "/operations/" + name, nil
}

func (a *action) firewallOperation(request contracts.ActionRequest, data map[string]any, operationType, requestID string) (string, error) {
	operation, err := a.firewallOperationURL(text(data["name"]))
	if err != nil {
		return "", err
	}
	if value, present := data["selfLink"]; present && a.client.canonicalName(text(value)) != a.client.canonicalName(operation) {
		return "", groupDenied("firewall_operation_scope_changed")
	}
	parent, _, _ := a.client.firewallIdentityParts(a.kind.NativeType, a.identity.NativeID)
	uid := request.Asset.Normalized["firewallPolicyId"]
	if isFirewallPolicy(a.kind.NativeType) {
		uid = request.Asset.Normalized["id"]
	}
	if a.client.canonicalName(text(data["targetLink"])) != parent || data["targetId"] != uid {
		return "", groupDenied("firewall_operation_target_changed")
	}
	if value, present := data["clientOperationId"]; present && value != a.firewallRequestID(request) {
		return "", groupDenied("firewall_operation_request_changed")
	}
	// operationType is a native open string. Persist the actual mutation's value,
	// then require polling to agree instead of inventing an undocumented enum.
	if text(data["operationType"]) == "" || operationType != "" && data["operationType"] != operationType || !slices.Contains([]string{"PENDING", "RUNNING", "DONE"}, text(data["status"])) {
		return "", groupDenied("firewall_operation_state_invalid")
	}
	if _, present := data["error"]; present {
		if err := operationError(data, requestID); err != nil {
			return "", err
		}
		return "", groupDenied("firewall_operation_error_invalid")
	}
	if value, present := data["httpErrorStatusCode"]; present && value != float64(0) && value != 0 {
		return "", groupDenied("firewall_operation_http_error")
	}
	if value, present := data["httpErrorMessage"]; present && value != "" {
		return "", groupDenied("firewall_operation_http_error")
	}
	return operation, nil
}

func (a *action) firewallPhase(request contracts.ActionRequest, operation, operationType string) map[string]any {
	return map[string]any{"phase": "firewall_delete", "resource": a.identity.NativeID, "kind": a.kind.NativeType, "snapshot": request.Asset.Normalized[firewallSnapshotKey], "review": serviceReview(request), "operation": operation, "operation_type": operationType, "request_id": a.firewallRequestID(request)}
}

func (a *action) executeFirewall(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	parameters := cloneParameters(a.deleteParameters)
	parameters["requestId"] = a.firewallRequestID(request)
	bound, err := catalog.BindREST(a.deleteOperation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if isNotFound(err) {
		read, err := a.firewallReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if read.Exists {
			return contracts.ActionResult{}, groupDenied("firewall_delete_not_observed")
		}
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, err := a.firewallOperation(request, response.Data, "", response.RequestID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: operation, Data: a.firewallPhase(request, operation, text(response.Data["operationType"])), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitFirewall(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if _, err := a.firewallActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	pending := false
	if len(result.Data) != 0 || result.ProviderOperationID != "" {
		u, err := url.Parse(result.ProviderOperationID)
		if err != nil {
			return contracts.WaitResult{}, groupDenied("firewall_operation_url_invalid")
		}
		expected, err := a.firewallOperationURL(last(u.Path))
		if err != nil || result.ProviderOperationID != expected || text(result.Data["operation_type"]) == "" || firewallDigest(result.Data) != firewallDigest(a.firewallPhase(request, expected, text(result.Data["operation_type"]))) {
			return contracts.WaitResult{}, groupDenied("firewall_phase_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.firewallOperation(request, response.Data, text(result.Data["operation_type"]), response.RequestID)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != expected {
				return contracts.WaitResult{}, groupDenied("firewall_operation_changed")
			}
			pending = response.Data["status"] != "DONE"
		}
	}
	read, err := a.firewallReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !pending && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
