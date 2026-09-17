package gcp

import (
	"context"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// OS Login SSH public keys are the Google Cloud equivalent of instance key
// pairs. Only the connection service account's own login profile is managed:
// another user's keys are neither listed nor mutated.
const osLoginKeyType = "oslogin.googleapis.com/SshPublicKey"
const osLoginSource = "oslogin-profile"
const osLoginReview = "_oslogin_key_configuration"
const osLoginPrefix = "//oslogin.googleapis.com/"

var osLoginFingerprint = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func (c *client) osLoginUser() (string, error) {
	email := strings.ToLower(strings.TrimSpace(c.email))
	if email == "" || !strings.Contains(email, "@") || strings.ContainsAny(email, "/ ") {
		return "", groupDenied("oslogin_identity_unavailable")
	}
	return "users/" + email, nil
}

// osLoginKeyName validates a native identity against the connection identity.
func (c *client) osLoginKeyName(id string) (string, error) {
	name := strings.TrimPrefix(id, osLoginPrefix)
	parts := strings.Split(name, "/")
	user, err := c.osLoginUser()
	if err != nil {
		return "", err
	}
	if name == id || len(parts) != 4 || parts[0] != "users" || parts[2] != "sshPublicKeys" || !osLoginFingerprint.MatchString(parts[3]) {
		return "", groupDenied("oslogin_key_identity_invalid")
	}
	if !strings.EqualFold("users/"+parts[1], user) {
		return "", groupDenied("oslogin_key_user_changed")
	}
	return user + "/sshPublicKeys/" + parts[3], nil
}

func (c *client) osLoginOperation(metadata providerMetadata, nativeID, method string) (catalog.Operation, map[string]any, error) {
	name, err := c.osLoginKeyName(nativeID)
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	operationID := "oslogin.users.sshPublicKeys.get"
	if method == "DELETE" {
		operationID = "oslogin.users.sshPublicKeys.delete"
	} else if method != "GET" {
		return catalog.Operation{}, nil, groupDenied("oslogin_method_unsupported")
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok {
		return catalog.Operation{}, nil, groupDenied("oslogin_operation_missing")
	}
	return operation, map[string]any{"name": name}, nil
}

func (c *client) osLoginCall(ctx context.Context, operationID string, name string) (contracts.InvocationResult, error) {
	metadata, err := providerData()
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok {
		return contracts.InvocationResult{}, groupDenied("oslogin_operation_missing")
	}
	bound, err := catalog.BindREST(operation, map[string]any{"name": name})
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	return c.requestResult(ctx, bound.Method, bound.URL, nil, nil)
}

// osLoginKey checks that a key read or profile entry is the named key.
func osLoginKey(name string, data map[string]any) error {
	fingerprint := name[strings.LastIndex(name, "/")+1:]
	if !strings.EqualFold(text(data["name"]), name) || text(data["fingerprint"]) != fingerprint || text(data["key"]) == "" {
		return groupDenied("oslogin_key_response_invalid")
	}
	return nil
}

func osLoginReviewValue(data map[string]any) string {
	return firewallDigest(map[string]any{"name": strings.ToLower(text(data["name"])), "fingerprint": data["fingerprint"], "key": data["key"], "expirationTimeUsec": data["expirationTimeUsec"]})
}

func (r *Runtime) listOSLoginKeys(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	failed := contracts.InventoryBatch{}
	if request.Source != osLoginSource || request.ResourceKind == nil || request.ResourceKind.NativeType != osLoginKeyType || request.Cursor != "" || request.NetworkTarget != nil || request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeGlobal {
		return failed, groupDenied("oslogin_inventory_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeGlobal && !slices.Contains([]string{"global", c.project + "/global", c.number + "/global"}, request.Scope.NativeID) {
		return failed, groupDenied("oslogin_inventory_project_changed")
	}
	user, err := c.osLoginUser()
	if err != nil {
		return failed, err
	}
	known := map[string]string{}
	for _, id := range request.KnownNativeIDs {
		name, err := c.osLoginKeyName(id)
		if err != nil {
			return failed, err
		}
		if _, duplicate := known[id]; duplicate {
			return failed, groupDenied("oslogin_known_duplicate")
		}
		known[id] = name
	}
	profile, err := c.osLoginCall(ctx, "oslogin.users.getLoginProfile", user)
	if err != nil {
		return failed, err
	}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, RequestID: profile.RequestID, Complete: true}
	keys := object(profile.Data["sshPublicKeys"])
	if profile.Data["sshPublicKeys"] != nil && keys == nil {
		return failed, groupDenied("oslogin_profile_invalid")
	}
	listed := map[string]bool{}
	add := func(name string, data map[string]any) error {
		if err := osLoginKey(name, data); err != nil {
			return err
		}
		id := osLoginPrefix + name
		item, err := r.inventoryItem(c, map[string]any{"name": id, "assetType": osLoginKeyType, "resource": map[string]any{"data": data, "location": "global"}})
		if err != nil {
			return err
		}
		delete(item.Normalized, "project_id")
		delete(item.Normalized, "project_number")
		item.Normalized["_inventory_source"] = osLoginSource
		item.Normalized[osLoginReview] = osLoginReviewValue(data)
		item.Name = text(data["fingerprint"])
		listed[id] = true
		batch.Items = append(batch.Items, item)
		return nil
	}
	fingerprints := make([]string, 0, len(keys))
	for fingerprint := range keys {
		fingerprints = append(fingerprints, fingerprint)
	}
	slices.Sort(fingerprints)
	for _, fingerprint := range fingerprints {
		if !osLoginFingerprint.MatchString(fingerprint) {
			return failed, groupDenied("oslogin_profile_invalid")
		}
		if err := add(user+"/sshPublicKeys/"+fingerprint, object(keys[fingerprint])); err != nil {
			return failed, err
		}
	}
	// A known key missing from the profile is closed only after its own read
	// returns 404; a key visible to the direct read is kept.
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if listed[id] {
			continue
		}
		result, err := c.osLoginCall(ctx, "oslogin.users.sshPublicKeys.get", known[id])
		if isNotFound(err) {
			batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id)
			continue
		}
		if err != nil {
			return failed, err
		}
		if err := add(known[id], result.Data); err != nil {
			return failed, err
		}
	}
	return batch, nil
}

func (a *action) osLoginActionIdentity(request contracts.ActionRequest) (string, error) {
	if request.Asset.ID == "" || request.IdempotencyKey == "" || request.Action != "delete" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || len(request.Parameters)+len(request.PrerequisiteDeletions)+len(request.LifecycleImpacts) != 0 {
		return "", groupDenied("oslogin_action_review_changed")
	}
	if proof, err := hex.DecodeString(text(request.Asset.Normalized[osLoginReview])); err != nil || len(proof) != 32 {
		return "", groupDenied("oslogin_action_review_missing")
	}
	return a.client.osLoginKeyName(a.identity.NativeID)
}

func (a *action) osLoginReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	name, err := a.osLoginActionIdentity(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	result, err := a.client.osLoginCall(ctx, "oslogin.users.sshPublicKeys.get", name)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false, State: "absent", Data: map[string]any{"provider_request_id": result.RequestID}}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := osLoginKey(name, result.Data); err != nil {
		return contracts.ReadbackResult{}, err
	}
	// A replaced key or changed expiration is not the reviewed resource.
	if osLoginReviewValue(result.Data) != text(request.Asset.Normalized[osLoginReview]) {
		return contracts.ReadbackResult{}, groupDenied("oslogin_key_changed")
	}
	return contracts.ReadbackResult{Exists: true, Data: map[string]any{"provider_request_id": result.RequestID}}, nil
}

func (a *action) osLoginPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.osLoginReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func (a *action) executeOSLogin(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	read, err := a.osLoginReadback(ctx, request)
	if err != nil || !read.Exists {
		return contracts.ActionResult{}, err
	}
	name, err := a.osLoginActionIdentity(request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.osLoginCall(ctx, "oslogin.users.sshPublicKeys.delete", name)
	if isNotFound(err) {
		live, readErr := a.osLoginReadback(ctx, request)
		if readErr != nil {
			return contracts.ActionResult{}, readErr
		}
		if live.Exists {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
	} else if err != nil {
		return contracts.ActionResult{}, err
	} else if len(response.Data) != 0 {
		return contracts.ActionResult{}, groupDenied("oslogin_delete_response_invalid")
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, Data: map[string]any{"phase": "oslogin_key_delete", "review": text(request.Asset.Normalized[osLoginReview])}, RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitOSLogin(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if result.ProviderOperationID != "" || len(result.Data) != 0 && (text(result.Data["phase"]) != "oslogin_key_delete" || text(result.Data["review"]) != text(request.Asset.Normalized[osLoginReview])) {
		return contracts.WaitResult{}, groupDenied("oslogin_delete_receipt_changed")
	}
	read, err := a.osLoginReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
