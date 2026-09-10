package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// This composed extension fixture tests native selector preservation. It does
// not assert Azure Monitor currently offers logs for every Cosmos child kind.
func newDiagnosticCaseFixture(t *testing.T, alias bool) *diagnosticFixture {
	t.Helper()
	f := newDiagnosticFixture(t)
	old := slices.Sorted(maps.Keys(f.sources))[0]
	clear(f.sources)
	wire := "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/microsoft.documentdb/databaseaccounts/account-one/sqldatabases/Sales/containers/Orders/storedprocedures/CreateOrder"
	id := strings.ToLower(wire)
	f.sources[id] = map[string]any{"id": wire, "name": "CreateOrder", "type": cosmosStoredProcedureType, "properties": map[string]any{"resource": map[string]any{"id": "CreateOrder", "_rid": "native-incarnation", "body": "function () {}"}}}
	for key, raw := range f.settings {
		if strings.HasPrefix(key, old+"/") {
			newWire := wire + strings.TrimPrefix(key, old)
			raw["id"] = newWire
			delete(f.settings, key)
			f.settings[strings.ToLower(newWire)] = raw
		}
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path != id && !strings.HasPrefix(path, id+"/") {
			return nil, false
		}
		if cosmosWireSignature(req.URL.Path[:len(wire)]) != wire {
			t.Fatal("diagnostic source names were lowercased", req.Method, req.URL)
		}
		if path == id && req.Method == "GET" {
			if raw := f.sources[id]; raw != nil {
				copy := maps.Clone(raw)
				if alias {
					copy["id"] = strings.Replace(strings.Replace(wire, "/containers/", "/sqlContainers/", 1), "/storedprocedures/", "/sqlStoredProcedures/", 1)
				}
				return jsonResponse(200, copy, nil), true
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		return nil, false
	}
	return f
}

func diagnosticItemAsset(item contracts.InventoryItem) asset.Asset {
	return asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: diagnosticSettingsType, NativeID: item.NativeID}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities, Name: item.Name, Tags: item.Tags}
}

func TestDiagnosticNativeSourceNamesAndOrphanSelectors(t *testing.T) {
	for _, mode := range []string{"native", "source-alias", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticCaseFixture(t, mode == "source-alias")
			request := f.request()
			batch, err := f.list(t, request)
			if err != nil || len(batch.Items) != 3 {
				t.Fatal("case-sensitive source inventory failed", len(batch.Items), err)
			}
			request.KnownNativeMetadata = map[string]map[string]any{}
			for _, item := range batch.Items {
				request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
				request.KnownNativeMetadata[item.NativeID] = item.Normalized
			}
			if mode == "orphan" {
				clear(f.sources)
				clear(f.groups)
			}
			encoded, _ := json.Marshal(request)
			if err := json.Unmarshal(encoded, &request); err != nil {
				t.Fatal(err)
			}
			batch, err = f.list(t, request)
			if err != nil || len(batch.Items) != 3 {
				t.Fatal("known native selectors did not survive recovery", err)
			}
			var value asset.Asset
			for _, item := range batch.Items {
				if strings.Contains(item.NativeID, "/resourcegroups/") {
					value = diagnosticItemAsset(item)
					break
				}
			}
			if !strings.Contains(text(value.Normalized[diagnosticWireSelector]), "/Sales/containers/Orders/storedprocedures/CreateOrder/") {
				t.Fatal("native source selector missing from normalized state")
			}
			f.hold = true
			driver := f.action(t, value)
			actionRequest := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-source-case"}
			result, err := driver.Execute(t.Context(), actionRequest)
			if err != nil || len(f.deleted) != 1 {
				t.Fatal("native source deletion failed", result, err)
			}
			encoded, _ = json.Marshal(result)
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			driver = f.action(t, value)
			if wait, err := driver.Wait(t.Context(), actionRequest, result); err != nil || wait.Done {
				t.Fatal("live native setting was considered absent", wait, err)
			}
			delete(f.settings, value.Identity.NativeID)
			if wait, err := driver.Wait(t.Context(), actionRequest, result); err != nil || !wait.Done {
				t.Fatal("native source absence did not complete recovered deletion", wait, err)
			}
		})
	}
}

func TestDiagnosticNativeSelectorTamperingCannotProveAbsence(t *testing.T) {
	for _, mode := range []string{"wire", "binding", "configuration", "context", "missing", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticCaseFixture(t, false)
			batch, err := f.list(t, f.request())
			if err != nil {
				t.Fatal(err)
			}
			var value asset.Asset
			for _, item := range batch.Items {
				if strings.Contains(item.NativeID, "/resourcegroups/") {
					value = diagnosticItemAsset(item)
					break
				}
			}
			driver := f.action(t, value)
			clear(f.settings)
			clear(f.sources)
			clear(f.groups)
			switch mode {
			case "wire":
				value.Normalized[diagnosticWireSelector] = strings.ToLower(text(value.Normalized[diagnosticWireSelector]))
			case "binding":
				value.Normalized[diagnosticWireProof] = "forged"
			case "configuration":
				value.Normalized[diagnosticConfigurationProof] = "forged"
			case "context":
				value.Normalized[diagnosticContextProof] = "forged"
			case "missing":
				delete(value.Normalized, diagnosticWireSelector)
			case "foreign":
				value.Normalized[diagnosticWireSelector] = strings.Replace(text(value.Normalized[diagnosticWireSelector]), testSubscription, testTenant, 1)
			}
			clear(f.calls)
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
				t.Fatal("tampered native selector resolved an action")
			}
			if _, err := driver.Readback(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"}); err == nil {
				t.Fatal("tampered native selector proved absence")
			}
			request := f.request()
			request.KnownNativeIDs = []string{value.Identity.NativeID}
			request.KnownNativeMetadata = map[string]map[string]any{value.Identity.NativeID: value.Normalized}
			if _, err := f.list(t, request); err == nil || isNotFound(err) {
				t.Fatal("tampered saved selector became successful empty inventory", err)
			}
			if len(f.calls)+len(f.deleted) != 0 {
				t.Fatal("invalid native selector reached transport", f.calls, f.deleted)
			}
		})
	}
}
