package azure

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (f *diagnosticFixture) asset(t *testing.T, id string) asset.Asset {
	t.Helper()
	request := f.request()
	request.KnownNativeIDs = slices.Sorted(maps.Keys(f.settings))
	batch, err := f.list(t, request)
	if err != nil {
		t.Fatal("diagnostic asset projection failed", err)
	}
	for _, item := range batch.Items {
		if item.NativeID == id {
			return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: diagnosticSettingsType, NativeID: id}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities, Name: item.Name, Tags: item.Tags}
		}
	}
	t.Fatal("diagnostic fixture asset missing", id)
	return asset.Asset{}
}

func (f *diagnosticFixture) action(t *testing.T, value asset.Asset) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal("diagnostic native action binding failed", err)
	}
	return driver
}

func TestDiagnosticActionNativeAbsenceAndRecovery(t *testing.T) {
	for _, mode := range []string{"resource-200", "resource-204", "subscription-200", "subscription-204", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticFixture(t)
			id := slices.Sorted(maps.Keys(f.settings))[1]
			if strings.HasPrefix(mode, "subscription") {
				id = "/subscriptions/" + testSubscription + "/providers/microsoft.insights/diagnosticsettings/subscription-setting"
			}
			if mode == "orphan" {
				clear(f.sources)
				clear(f.groups)
			}
			if strings.HasSuffix(mode, "200") {
				f.deleteStatus = 200
			}
			value := f.asset(t, id)
			driver := f.action(t, value)
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "reviewed-diagnostic-delete"}
			f.hold = true
			result, err := driver.Execute(t.Context(), request)
			if err != nil || result.ProviderRequestID != "diagnostic-native-delete" || result.ProviderOperationID != "" || text(result.Data["_diagnostic_delete_receipt"]) == "" || !slices.Equal(f.deleted, []string{id}) {
				t.Fatal("native diagnostic deletion failed", result, err, f.deleted)
			}
			// A recovered worker has only the JSON request/result and credentials.
			encoded, _ := json.Marshal(request)
			if err := json.Unmarshal(encoded, &request); err != nil {
				t.Fatal(err)
			}
			encoded, _ = json.Marshal(result)
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			driver = f.action(t, request.Asset)
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil || wait.Done {
				t.Fatal("synchronous DELETE was mistaken for setting absence", wait, err)
			}
			if strings.HasPrefix(mode, "resource") {
				clear(f.sources)
				clear(f.groups)
				if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
					t.Fatal("source/group disappearance completed diagnostic deletion", wait, err)
				}
			}
			delete(f.settings, id)
			wait, err = driver.Wait(t.Context(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("native diagnostic absence did not complete recovered deletion", wait, err)
			}
			request.ExecutionResult = &result
			if read, err := driver.Readback(t.Context(), request); err != nil || read.Exists {
				t.Fatal("diagnostic final absence failed", read, err)
			}
			for _, operation := range f.deleted {
				if operation != id {
					t.Fatal("diagnostic deletion removed a shared resource")
				}
			}
		})
	}
}

func TestDiagnosticActionDriftAndProtection(t *testing.T) {
	for _, mode := range []string{"private-change", "destination-change", "source-recreated", "source-gone", "group-change", "source-tag", "group-tag", "lock", "malformed-source-tags", "malformed-group-tags", "forged-reference", "changed-context", "foreign-identity", "extra-impact"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticFixture(t)
			source := slices.Sorted(maps.Keys(f.sources))[0]
			group := slices.Sorted(maps.Keys(f.groups))[0]
			id := source + "/providers/microsoft.insights/diagnosticsettings/resource-a"
			value := f.asset(t, id)
			driver := f.action(t, value)
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			switch mode {
			case "private-change":
				object(f.settings[id]["properties"])["ordinaryAuthoredField"] = "private-change"
			case "destination-change":
				object(f.settings[id]["properties"])["storageAccountId"] = resourceID(storageType, "different")
			case "source-recreated":
				object(f.sources[source]["systemData"])["createdAt"] = "2026-02-01T00:00:00Z"
			case "source-gone":
				delete(f.sources, source)
			case "group-change":
				f.groups[group]["managedBy"] = resourceID(aksType, "new-controller")
			case "source-tag":
				f.sources[source]["tags"] = map[string]any{"steward:protected": "true"}
			case "group-tag":
				f.groups[group]["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": group + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "ReadOnly"}}}
			case "malformed-source-tags":
				f.sources[source]["tags"] = map[string]any{"steward:protected": true}
			case "malformed-group-tags":
				f.groups[group]["tags"] = "opaque"
			case "forged-reference":
				object(request.Asset.Normalized["_diagnostic_references"])[storageType] = []string{strings.ToLower(resourceID(storageType, "forged"))}
			case "changed-context":
				request.Asset.Normalized[diagnosticContextProof] = "changed"
			case "foreign-identity":
				request.Asset.Identity.ConnectionID = "other"
			case "extra-impact":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: value, Delete: true}}
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) || len(f.deleted) != 0 {
				t.Fatal("diagnostic drift authorized a write", err, f.deleted)
			}
		})
	}
}

func TestDiagnosticActionRejectsUnboundReceiptsAfterAbsence(t *testing.T) {
	for _, mode := range []string{"missing", "changed", "operation", "other-setting", "changed-reference"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticFixture(t)
			id := slices.Sorted(maps.Keys(f.settings))[0]
			value := f.asset(t, id)
			driver := f.action(t, value)
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing":
				result.Data = nil
			case "changed":
				result.Data["_diagnostic_delete_receipt"] = "changed"
			case "operation":
				result.ProviderOperationID = "https://management.azure.com/operations/unrelated"
			case "other-setting":
				other := f.asset(t, slices.Sorted(maps.Keys(f.settings))[0])
				result.Data["_diagnostic_delete_receipt"] = monitorTargetInner(f.action(t, other)).(*diagnosticAction).receipt()
			case "changed-reference":
				request.Asset.Normalized["_diagnostic_references"] = map[string]any{}
			}
			before := maps.Clone(f.calls)
			if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
				t.Fatal("forged diagnostic recovery completed", wait, err)
			}
			if !maps.Equal(before, f.calls) {
				t.Fatal("invalid diagnostic recovery reached cloud transport")
			}
		})
	}
}

func TestDiagnosticPayloadPrivacyAndNativeErrorCode(t *testing.T) {
	for _, operation := range []string{"DiagnosticSettings_List", "SubscriptionDiagnosticSettings_List"} {
		t.Run(operation, func(t *testing.T) {
			private := map[string]any{"name": "selected", "ordinaryAuthoredField": "private-top-level", "properties": map[string]any{"ordinaryAuthoredField": "private-property", "logs": []any{map[string]any{"category": "private-category", "enabled": true}}}}
			before, _ := json.Marshal(private)
			var entries []execution.JobLogEntry
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if !diagnosticSettingsPath(req.URL.Path) || req.Method != "GET" {
					t.Fatal("unexpected diagnostic invocation", req.URL)
				}
				return jsonResponse(200, map[string]any{"value": []any{private}}, http.Header{"X-Ms-Request-Id": {"diagnostic-native-response"}}), nil
			})
			params := map[string]any{}
			if operation == "DiagnosticSettings_List" {
				params["resourceUri"] = strings.TrimPrefix(resourceID("Microsoft.KeyVault/vaults", "source"), "/")
			}
			result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Insights." + operation, Parameters: params})
			if err != nil || result.RequestID != "diagnostic-native-response" || text(object(array(result.Data["value"])[0])["name"]) != "selected" {
				t.Fatal("diagnostic invocation lost public provenance", result, err)
			}
			for _, value := range []any{entries, result, safePayload(map[string]any{"type": diagnosticSettingsType, "properties": private}), safePayload(map[string]any{"id": resourceID(diagnosticSettingsType, "setting"), "properties": private})} {
				encoded, _ := json.Marshal(value)
				if strings.Contains(string(encoded), "private-") {
					t.Fatal("private diagnostic content leaked", string(encoded))
				}
			}
			after, _ := json.Marshal(private)
			if string(before) != string(after) {
				t.Fatal("diagnostic redaction modified native data")
			}
		})
	}
	id := strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "source") + "/providers/" + diagnosticSettingsType + "/setting")
	c := directClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(400, map[string]any{"code": "ResourceTypeNotSupported", "message": "private-native-message"}, nil), nil
	})
	_, err := c.diagnosticRead(t.Context(), id, diagnosticSettingsType)
	var failure *contracts.ProviderCallError
	if !errors.As(err, &failure) || failure.Provider.Code != "ResourceTypeNotSupported" || strings.Contains(err.Error(), "private-native-message") {
		t.Fatal("diagnostic native error code was lost or message leaked", err)
	}
}
