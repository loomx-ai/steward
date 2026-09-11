package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func communicationPlan(t *testing.T, f *communicationFixture, values []asset.Asset, targets ...asset.AssetID) plan.Result {
	t.Helper()
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatal("native Communication graph", contribution.Unresolved, err)
	}
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: targets, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) != 0 {
		t.Fatal("native Communication plan", result.Blockers, err)
	}
	return result
}

func TestCommunicationReviewedNativeDeletion(t *testing.T) {
	f := newCommunicationFixture(t)
	values := f.assets(t)
	comm, email := cdnAsset(t, values, communicationType), cdnAsset(t, values, communicationEmailType)
	solved := communicationPlan(t, f, values, comm.ID, email.ID)
	original := f.override
	operations := map[string]string{}
	deletions := map[string]int{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		key := strings.ToLower(req.URL.Path)
		if req.URL.Host != "management.azure.com" {
			key = "https://" + req.URL.Host + req.URL.Path
		}
		if id := operations[req.URL.Path]; id != "" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != communicationARMVersion {
				t.Fatal("wrong native polling request")
			}
			return jsonResponse(200, map[string]any{"id": req.URL.Path, "name": last(req.URL.Path), "resourceId": id, "status": "Succeeded"}, nil), true
		}
		if req.Method == "DELETE" {
			deletions[key]++
			if f.resources[key] == nil || f.kinds[key] == communicationPhoneType {
				t.Fatal("unreviewed or duplicate native DELETE", key)
			}
			if req.Header.Get("x-ms-client-request-id") != azureRequestID("native-delete-"+key) {
				t.Fatal("native delete lost its request id")
			}
			delete(f.resources, key)
			kind := f.kinds[key]
			if kind == communicationType || kind == communicationEmailType || kind == communicationDomainType {
				path := "/providers/Microsoft.Communication/locations/westus/operationStatuses/" + azureRequestID(key)
				operations[path] = key
				return jsonResponse(202, nil, http.Header{"Location": {"https://management.azure.com" + path}}), true
			}
			return jsonResponse(204, nil, nil), true
		}
		return original(req)
	}
	for _, step := range solved.Steps {
		value := values[slices.IndexFunc(values, func(v asset.Asset) bool { return v.ID == step.AssetID })]
		request := servicePlanRequest(solved, values, value)
		request.IdempotencyKey = "native-delete-" + value.Identity.NativeID
		driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
		if err != nil {
			t.Fatal("resolve native Communication action", value.Identity.NativeType, err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("execute native Communication deletion", value.Identity.NativeType, err)
		}
		waited, err := driver.Wait(t.Context(), request, result)
		if err != nil {
			t.Fatal("wait native Communication deletion", value.Identity.NativeType, err)
		}
		if value.Identity.NativeType == communicationType {
			if waited.Done || object(waited.Data)["communication_phase"] != "absence" || f.calls["GET "+f.ids[communicationPhoneType]] == 0 {
				t.Fatal("account 404 concealed a live released phone", waited)
			}
			if driver.(contracts.DeletionCheckTimeoutProvider).DeletionCheckTimeout() < 32*24*time.Hour {
				t.Fatal("phone verification cannot span the native billing window")
			}
			result.Data = waited.Data
			delete(f.resources, f.ids[communicationPhoneType])
		} else if !waited.Done {
			t.Fatal("deleted native resource remained pending", value.Identity.NativeType, waited)
		}
		payload, _ := json.Marshal(result)
		if err := json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		payload, _ = json.Marshal(request)
		if err := json.Unmarshal(payload, &request); err != nil {
			t.Fatal(err)
		}
		request.ExecutionResult = &result
		driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := driver.Execute(t.Context(), request)
		if err != nil || resumed.ProviderOperationID != result.ProviderOperationID || deletions[value.Identity.NativeID] != 1 {
			t.Fatal("resumed action repeated DELETE", err, deletions)
		}
		waited, err = driver.Wait(t.Context(), request, result)
		if err != nil || !waited.Done {
			t.Fatal("resumed native absence", value.Identity.NativeType, waited, err)
		}
	}
	if len(deletions) != 9 || len(f.resources) != 0 {
		t.Fatal("reviewed cleanup left native resources or lost direct steps", deletions, len(f.resources))
	}
}

func TestCommunicationDirectPhoneRelease(t *testing.T) {
	for _, mode := range []string{"running-succeeded", "expired-operation", "succeeded-phone-present", "phone-recreated", "changed-account", "search-operation", "wrong-operation-id", "failed", "unknown-state", "missing-time", "foreign-result-url", "forged-receipt", "changed-phase", "root-404-phone-403"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			values := f.assets(t)
			phone := cdnAsset(t, values, communicationPhoneType)
			request := contracts.ActionRequest{Asset: phone, Action: "delete", IdempotencyKey: "release-phone"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", phone)
			if err != nil {
				t.Fatal(err)
			}
			operationID := "release_378ddf60-81be-452a-ba4f-613198ea6c28"
			operationPath := "/phoneNumbers/operations/" + operationID
			deletes, polls := 0, 0
			state := "running"
			original := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if "https://"+req.URL.Host+req.URL.Path != phone.Identity.NativeID || req.Header.Get("x-ms-client-request-id") != azureRequestID(request.IdempotencyKey) {
						t.Fatal("phone release escaped selected native URL")
					}
					deletes++
					return jsonResponse(202, nil, http.Header{"Operation-Location": {operationPath}, "Operation-Id": {operationID}}), true
				}
				if req.URL.Path == operationPath {
					polls++
					if mode == "expired-operation" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), true
					}
					raw := map[string]any{"id": operationID, "operationType": "releasePhoneNumber", "status": state, "createdDateTime": "2026-09-10T01:02:03Z"}
					headers := http.Header{}
					switch mode {
					case "search-operation":
						raw["operationType"] = "search"
					case "wrong-operation-id":
						raw["id"] = "another-operation"
					case "failed":
						raw["status"] = "failed"
						raw["error"] = map[string]any{"code": "Failed", "message": "private phone details"}
					case "unknown-state":
						raw["status"] = "pendingReview"
					case "missing-time":
						delete(raw, "createdDateTime")
					case "foreign-result-url":
						raw["resourceLocation"] = "https://unrelated.invalid/private"
						headers.Set("Location", "https://unrelated.invalid/private")
					}
					return jsonResponse(200, raw, headers), true
				}
				return original(req)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || deletes != 1 || result.ProviderOperationID == "" {
				t.Fatal("native release receipt", result, err, deletes)
			}
			switch mode {
			case "phone-recreated":
				f.resources[phone.Identity.NativeID]["purchaseDate"] = "2026-09-11T00:00:00Z"
			case "changed-account":
				object(f.resources[f.ids[communicationType]]["properties"])["futurePrivateSetting"] = "changed"
			case "forged-receipt":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "communication.azure.com", "communication.azure.com.evil.invalid", 1)
			case "changed-phase":
				result.Data["communication_phase"] = "absence"
			case "root-404-phone-403":
				delete(f.resources, f.ids[communicationType])
				before := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if "https://"+req.URL.Host+req.URL.Path == phone.Identity.NativeID {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return before(req)
				}
			}
			state = "succeeded"
			if mode == "running-succeeded" {
				state = "running"
			}
			waited, err := driver.Wait(t.Context(), request, result)
			invalid := slices.Contains([]string{"phone-recreated", "changed-account", "search-operation", "wrong-operation-id", "failed", "unknown-state", "missing-time", "forged-receipt", "changed-phase", "root-404-phone-403"}, mode)
			if invalid {
				if err == nil {
					t.Fatal("invalid release evidence accepted", mode, waited)
				}
				return
			}
			if err != nil || waited.Done {
				t.Fatal("release status proved absence of a live phone", waited, err)
			}
			if waited.Data != nil {
				result.Data = waited.Data
			}
			if mode != "running-succeeded" && result.Data["communication_phase"] != "absence" {
				t.Fatal("terminal release did not persist absence phase")
			}
			state = "succeeded"
			delete(f.resources, phone.Identity.NativeID)
			payload, _ := json.Marshal(result)
			json.Unmarshal(payload, &result)
			request.ExecutionResult = &result
			driver, err = f.runtime.ResolveAction(t.Context(), "connection", phone)
			if err != nil {
				t.Fatal(err)
			}
			before := polls
			resumed, err := driver.Execute(t.Context(), request)
			if err != nil || deletes != 1 {
				t.Fatal("resumed phone repeated release", err, deletes)
			}
			waited, err = driver.Wait(t.Context(), request, resumed)
			if err != nil || !waited.Done || mode != "running-succeeded" && polls != before {
				t.Fatal("resumed native phone 404 not verified", waited, err, polls, before)
			}
		})
	}
}

func TestCommunicationActionBoundaries(t *testing.T) {
	for _, mode := range []string{"missing-impact", "retained-phone", "changed-controller", "extra-prerequisite", "missing-prerequisite", "foreign-prerequisite", "changed-private-config", "late-leaf-change", "new-child", "ancestor-protected", "group-changed", "locked", "reservation-purchasing", "changed-normalized", "changed-location", "changed-connection", "wrong-runtime-connection", "changed-asset-id", "already-deleting", "parent-absent-live-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			values := f.assets(t)
			comm := cdnAsset(t, values, communicationType)
			solved := communicationPlan(t, f, values, comm.ID)
			value := comm
			if slices.Contains([]string{"ancestor-protected", "late-leaf-change", "parent-absent-live-child"}, mode) {
				value = cdnAsset(t, values, communicationRoomType)
			}
			if mode == "reservation-purchasing" {
				value = cdnAsset(t, values, communicationReservationType)
			}
			request := servicePlanRequest(solved, values, value)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{communicationSMTPType, communicationRoomType, communicationReservationType} {
				if value.Identity.NativeType == communicationType {
					delete(f.resources, f.ids[kind])
				}
			}
			switch mode {
			case "missing-impact":
				request.LifecycleImpacts = nil
			case "retained-phone":
				request.LifecycleImpacts[0].Delete = false
			case "changed-controller":
				request.LifecycleImpacts[0].ControllerID = "other"
			case "extra-prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: cdnAsset(t, values, communicationEmailType), ControllerID: comm.ID, Delete: true})
			case "missing-prerequisite":
				request.PrerequisiteDeletions = request.PrerequisiteDeletions[1:]
			case "foreign-prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "foreign"
			case "changed-private-config":
				object(f.resources[f.ids[communicationType]]["properties"])["futurePrivateSetting"] = "changed"
			case "late-leaf-change":
				before := f.override
				f.calls = map[string]int{}
				f.override = func(req *http.Request) (*http.Response, bool) {
					if "https://"+req.URL.Host+req.URL.Path == value.Identity.NativeID && f.calls["GET "+value.Identity.NativeID] == 3 {
						f.resources[value.Identity.NativeID]["futurePrivateSetting"] = "changed"
					}
					return before(req)
				}
			case "new-child":
				id := f.ids[communicationRoomType] + "New"
				raw := communicationBody(t, "Rooms_Get")
				raw["id"] = last(id)
				f.resources[id], f.kinds[id] = raw, communicationRoomType
			case "ancestor-protected":
				f.resources[comm.Identity.NativeID]["tags"] = map[string]any{"steward:protect": "true"}
			case "group-changed":
				f.group["managedBy"] = "external-owner"
			case "locked":
				f.locks = []any{map[string]any{"id": comm.Identity.NativeID + "/providers/microsoft.authorization/locks/retain", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "reservation-purchasing":
				f.resources[value.Identity.NativeID]["status"] = "submitted"
			case "changed-normalized":
				request.Asset.Normalized[communicationConfiguration] = "forged"
			case "changed-location":
				request.Asset.Location = "westus"
			case "changed-connection":
				request.Asset.Identity.ConnectionID = "different"
			case "wrong-runtime-connection":
				if _, err := f.runtime.ResolveAction(t.Context(), "different", value); err == nil {
					t.Fatal("action accepted another runtime connection")
				}
				return
			case "changed-asset-id":
				request.Asset.ID = "different"
			case "already-deleting":
				object(f.resources[comm.Identity.NativeID]["properties"])["provisioningState"] = "Deleting"
			case "parent-absent-live-child":
				delete(f.resources, comm.Identity.NativeID)
			}
			_, err = driver.Execute(t.Context(), request)
			if mode == "already-deleting" || mode == "parent-absent-live-child" {
				if err != nil {
					t.Fatal("native in-progress cleanup did not resume as read-only verification", err)
				}
			} else if err == nil {
				t.Fatal("changed Communication deletion boundary accepted", mode)
			}
			for key := range f.calls {
				if strings.HasPrefix(key, "DELETE ") {
					t.Fatal("invalid or already deleting action issued another DELETE", key)
				}
			}
		})
	}
}

// Preserve the native URL's query across serialization, including signed
// ProviderHub receipts, rather than reconstructing a poll from a resource ID.
func communicationSignedPollURL(id, signature string) string {
	return "https://management.azure.com/subscriptions/" + testSubscription + "/providers/Microsoft.Communication/locations/WESTUS2/operationStatuses/" + azureRequestID(id) + "*" + strings.Repeat("A", 64) + "?api-version=" + communicationARMVersion + "&t=123&c=certificate&s=" + url.QueryEscape(signature) + "&h=hash"
}

func TestCommunicationARMPollRotation(t *testing.T) {
	f := newCommunicationFixture(t)
	values := f.assets(t)
	// Use an email root with its reviewed descendant deletions already absent.
	value := cdnAsset(t, values, communicationEmailType)
	request := contracts.ActionRequest{Asset: value, Action: "delete"}
	for _, member := range values {
		if member.Normalized["_communication_parent"] == value.Identity.NativeID {
			request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: member, ControllerID: value.ID, Delete: true})
		}
	}
	for id := range object(value.Normalized[communicationMembers]) {
		delete(f.resources, id)
	}
	original := f.override
	first, next := communicationSignedPollURL(value.Identity.NativeID, "original"), communicationSignedPollURL(value.Identity.NativeID, "rotated")
	op, _ := url.Parse(first)
	polls, deletes := 0, 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "DELETE" {
			deletes++
			return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {first}, "Location": {communicationSignedPollURL(value.Identity.NativeID, "secondary")}}), true
		}
		if req.URL.Path == op.Path {
			polls++
			if polls == 1 && req.URL.String() != first || polls == 2 && req.URL.String() != next {
				t.Fatal("poll signature was not preserved across restart")
			}
			state := "Deleting"
			headers := http.Header{"Azure-Asyncoperation": {next}, "Location": {communicationSignedPollURL(value.Identity.NativeID, "secondary-rotated")}}
			status := 202
			if polls == 2 {
				state, status, headers = "Succeeded", 200, nil
			}
			return jsonResponse(status, map[string]any{"id": op.Path, "name": last(op.Path), "resourceId": value.Identity.NativeID, "status": state, "startTime": "2026-09-10T01:02:03Z"}, headers), true
		}
		return original(req)
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	waited, err := driver.Wait(t.Context(), request, result)
	if err != nil || waited.Done || waited.Data["communication_operation"] != next {
		t.Fatal("native poll rotation", waited, err)
	}
	result.Data = waited.Data
	payload, _ := json.Marshal(result)
	json.Unmarshal(payload, &result)
	request.ExecutionResult = &result
	driver, _ = f.runtime.ResolveAction(t.Context(), "connection", value)
	forged := result
	forged.Data = maps.Clone(result.Data)
	forged.Data["communication_operation"] = first
	if _, err := driver.Wait(t.Context(), request, forged); err == nil || polls != 1 {
		t.Fatal("forged successor performed polling")
	}
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || waited.Done || waited.Data["communication_phase"] != "absence" {
		t.Fatal("operation success hid a live email resource", waited, err)
	}
	result.Data = waited.Data
	request.ExecutionResult = &result
	delete(f.resources, value.Identity.NativeID)
	if _, err := driver.Execute(t.Context(), request); err != nil || deletes != 1 {
		t.Fatal("persisted terminal LRO repeated DELETE", err, deletes)
	}
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || !waited.Done || polls != 2 {
		t.Fatal("native own-404 did not finish after terminal LRO", waited, err, polls)
	}
}

func TestCommunicationReadbackRequiresEachOwnResource404(t *testing.T) {
	for _, mode := range []string{"room-roster-404", "room-roster-403", "room-own-404", "account-gone-room-roster-404", "email-gone-grandchild-live", "email-gone-grandchild-403", "email-family-gone", "late-unreviewed-room"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			values := f.assets(t)
			comm, email := cdnAsset(t, values, communicationType), cdnAsset(t, values, communicationEmailType)
			solved := communicationPlan(t, f, values, comm.ID, email.ID)
			value := cdnAsset(t, values, communicationRoomType)
			if strings.HasPrefix(mode, "email-") {
				value = email
			}
			if mode == "account-gone-room-roster-404" || mode == "late-unreviewed-room" {
				value = comm
			}
			request := servicePlanRequest(solved, values, value)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			original := f.override
			if mode == "room-own-404" {
				delete(f.resources, value.Identity.NativeID)
			} else if mode == "late-unreviewed-room" {
				id := f.ids[communicationRoomType] + "New"
				raw := communicationBody(t, "Rooms_Get")
				raw["id"] = last(id)
				f.resources[id], f.kinds[id] = raw, communicationRoomType
			} else if strings.HasPrefix(mode, "email-") {
				delete(f.resources, email.Identity.NativeID)
				for id, entry := range object(email.Normalized[communicationMembers]) {
					if object(entry)["kind"] != communicationAddressType || mode == "email-family-gone" {
						delete(f.resources, id)
					}
				}
				if mode == "email-gone-grandchild-403" {
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, f.ids[communicationAddressType]) {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
						}
						return original(req)
					}
				}
			} else {
				if mode == "account-gone-room-roster-404" {
					delete(f.resources, comm.Identity.NativeID)
					for _, kind := range []string{communicationSMTPType, communicationPhoneType, communicationReservationType} {
						delete(f.resources, f.ids[kind])
					}
				}
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/participants") {
						status := 404
						if mode == "room-roster-403" {
							status = 403
						}
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "Unavailable"}}, nil), true
					}
					return original(req)
				}
			}
			read, err := driver.Readback(t.Context(), request)
			valid := slices.Contains([]string{"room-own-404", "email-gone-grandchild-live", "email-family-gone"}, mode)
			if valid {
				if err != nil || read.Exists != (mode == "email-gone-grandchild-live") {
					t.Fatal("native own-resource absence disagrees", read, err)
				}
			} else if err == nil || isNotFound(err) {
				t.Fatal("missing or unreadable dependency became target absence", read, err)
			}
		})
	}
}
