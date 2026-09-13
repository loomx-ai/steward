package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestAzureLocalNetworkTypeInventory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		native  any
		role    string
		invalid bool
	}{
		{"workload", "Workload", "Workload", false},
		{"infrastructure", "Infrastructure", "Infrastructure", false},
		{"missing", nil, "Unknown", false},
		{"empty", "", "Unknown", false},
		{"future", "FutureType", "Unknown", false},
		{"case", "workload", "Unknown", false},
		{"whitespace", " Workload ", "Unknown", false},
		{"boolean", false, "", true},
		{"object", map[string]any{"name": "Workload"}, "", true},
		{"array", []any{"Workload"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAzureLocalFixture(t)
			id := f.ids[azureLocalNetworkType]
			raw := f.values[id]
			props := object(raw["properties"])
			if tc.native != nil {
				props["networkType"] = tc.native
			}
			// Neither the resource's label nor deployment/user-controlled metadata
			// can substitute for the native read-only discriminator.
			raw["tags"] = map[string]any{"networkType": "Workload", "infrastructure": "false"}
			props["vmSwitchName"] = "Workload"
			props["privateNetworkCredential"] = "private-network-token"
			request := f.request(azureLocalNetworkType)
			batch, err := f.runtime.List(t.Context(), request)
			if tc.invalid {
				if err == nil || batch.Complete || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("malformed type completed inventory", batch, err)
				}
				return
			}
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			if item.Normalized["networkType"] != tc.role || item.Actionable == nil || *item.Actionable != (tc.role != "Unknown") {
				t.Fatal("network role/capability", item)
			}
			value := asset.Asset{ID: "network", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: azureLocalNetworkType, NativeID: id}, Location: item.Location, Normalized: item.Normalized}
			if _, err := f.client.azureLocalRecordedReferences(value); err != nil {
				t.Fatal(err)
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); (err == nil) != (tc.role != "Unknown") {
				t.Fatal("unverified network type enabled deletion")
			}
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "private-network-token") {
				t.Fatal("private network configuration leaked")
			}
			// Previously observed networks survive an omitted list and JSON recovery.
			var restored map[string]any
			encoded, _ = json.Marshal(item.Normalized)
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			request.KnownNativeIDs = []string{id}
			request.KnownNativeMetadata = map[string]map[string]any{id: restored}
			f.omitted[id] = true
			batch, err = f.runtime.List(t.Context(), request)
			if err != nil || !batch.Complete || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 || batch.Items[0].Normalized["networkType"] != tc.role {
				t.Fatal("known network recovery", batch, err)
			}
			for path := range f.reads {
				if strings.Contains(path, "microsoft.hybridcompute") && tc.role != "Infrastructure" {
					t.Fatal("classification made unrelated dependency reads", path)
				}
			}
		})
	}
}

func TestAzureLocalNetworkReadBoundaries(t *testing.T) {
	for _, mode := range []string{"denied", "unsupported_version", "async_get", "changed_type", "wrong_identity", "wrong_index_type", "cursor_version", "cursor_host", "cursor_loop", "known_denied", "known_absent"} {
		t.Run(mode, func(t *testing.T) {
			f := newAzureLocalFixture(t)
			id := f.ids[azureLocalNetworkType]
			object(f.values[id]["properties"])["networkType"] = "Infrastructure"
			request := f.request(azureLocalNetworkType)
			initial, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "known_") {
				request.KnownNativeIDs = []string{id}
				request.KnownNativeMetadata = map[string]map[string]any{id: initial.Items[0].Normalized}
				f.omitted[id] = true
			}
			collection := f.client.root() + "/providers/microsoft.azurestackhci/logicalnetworks"
			calls := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path != id && path != collection {
					return nil, false
				}
				calls++
				if req.Method != "GET" || req.URL.Query().Get("api-version") != "2025-06-01-preview" {
					t.Fatal("network version changed", req.URL)
				}
				switch mode {
				case "denied", "known_denied":
					if path == id {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
				case "unsupported_version":
					return jsonResponse(400, map[string]any{"error": map[string]any{"code": "InvalidApiVersionParameter"}}, nil), true
				case "known_absent":
					if path == id {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
				case "async_get":
					if path == id {
						return jsonResponse(200, f.values[id], http.Header{"Azure-Asyncoperation": {"http://azure.async.operation/status"}}), true
					}
				case "changed_type":
					if path == id {
						raw := batchClone(f.values[id])
						object(raw["properties"])["networkType"] = "Workload"
						return jsonResponse(200, raw, nil), true
					}
				case "wrong_identity":
					if path == id {
						raw := batchClone(f.values[id])
						raw["id"] = strings.Replace(id, testSubscription, testTenant, 1)
						return jsonResponse(200, raw, nil), true
					}
				case "wrong_index_type":
					if path == collection {
						raw := batchClone(f.values[id])
						object(raw["properties"])["networkType"] = false
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
					}
				default:
					if path == collection {
						next := apiURL(collection, "2025-06-01-preview")
						if mode == "cursor_version" {
							next = apiURL(collection, "2024-01-01")
						}
						if mode == "cursor_host" {
							next = strings.Replace(next, "management.azure.com", "attacker.test", 1)
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), true
					}
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), request)
			if mode == "known_absent" {
				if err != nil || !batch.Complete || len(batch.Items) != 0 || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != id {
					t.Fatal("own absence not reconciled", batch, err)
				}
			} else if err == nil || batch.Complete || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("failed network read completed inventory", batch, err)
			}
			if calls == 0 || calls > 6 {
				t.Fatal("unbounded network requests", calls)
			}
		})
	}
}

// A changed discriminator invalidates a paginated snapshot, even when all
// resource IDs remain identical. It cannot be hidden by normalized metadata.
func TestAzureLocalNetworkTypeInvalidatesCursor(t *testing.T) {
	f := newAzureLocalFixture(t)
	id := f.ids[azureLocalNetworkType]
	object(f.values[id]["properties"])["networkType"] = "Workload"
	second := batchClone(f.values[id])
	secondID := id + "-two"
	second["id"], second["name"] = secondID, last(secondID)
	f.values[secondID] = second
	request := f.request(azureLocalNetworkType)
	request.Limit = 1
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || batch.Complete || batch.NextCursor == "" {
		t.Fatal(batch, err)
	}
	request.Cursor = batch.NextCursor
	object(f.values[id]["properties"])["networkType"] = "Infrastructure"
	batch, err = f.runtime.List(t.Context(), request)
	if err == nil || batch.Complete {
		t.Fatal("changed role resumed old cursor", batch, err)
	}
}
