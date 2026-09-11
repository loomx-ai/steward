package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (f *dataFactoryFixture) request(kind string) contracts.InventoryRequest {
	resource := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: dataFactoryInventorySource, ResourceKind: &resource, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func TestDataFactoryInventorySourceAllNativeKinds(t *testing.T) {
	f := newDataFactoryFixture(t)
	for kind, id := range f.ids {
		t.Run(kind, func(t *testing.T) {
			batch, err := f.runtime.List(t.Context(), f.request(kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal("incomplete native inventory source", err, len(batch.Items))
			}
			item := batch.Items[0]
			if item.NativeID != id || item.NativeType != kind || item.Location != "eastus" || item.Scope.Kind != asset.ScopeRegion || item.Scope.NativeID != "eastus" || item.Normalized["_inventory_source"] != dataFactoryInventorySource || item.Actionable == nil || *item.Actionable != (kind != dataFactoryNetworkType) {
				t.Fatal("native owner scope or actionability changed", kind, item.Location, item.Normalized["cleanup_protection_reason"])
			}
			if _, err := f.client.dataFactoryRecorded(id, kind, item.Normalized); err != nil {
				t.Fatal("inventory binding is not authenticated", err)
			}
			request := f.request(kind)
			request.KnownNativeIDs = []string{id}
			request.KnownNativeMetadata = map[string]map[string]any{id: item.Normalized}
			hints, err := f.client.dataFactoryKnown(request)
			if err != nil || hints[f.ids[dataFactoryType]].id == "" || hints[id].id != id {
				t.Fatal("known identity lost native ancestors", err)
			}
			payload, _ := json.Marshal(item)
			for _, hidden := range []string{"private-native-datafactory-config", "hostServiceUri", "commonDslConnectorProperties", "exampleoutput.csv"} {
				if strings.Contains(string(payload), hidden) {
					t.Fatal("authored configuration escaped private inventory", hidden)
				}
			}
			if kind == dataFactoryNodeType && object(item.Normalized["arm_parameters"])["nodeName"] != "Node_1" {
				t.Fatal("native node parameter case lost")
			}
			request = f.request(kind)
			request.Source = inventorySource
			duplicate, err := f.runtime.List(t.Context(), request)
			if err != nil || !duplicate.Complete || len(duplicate.Items) != 0 {
				t.Fatal("generic ARM source duplicated Data Factory", err)
			}
		})
	}
}

func TestDataFactoryInventorySourceKnownAbsence(t *testing.T) {
	for _, mode := range []string{"omitted", "gone", "forbidden", "forged-binding", "foreign-connection", "missing-metadata", "unrequested-metadata", "duplicate-id", "surviving-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			kind, id := dataFactoryDatasetType, f.ids[dataFactoryDatasetType]
			if mode == "surviving-child" {
				kind, id = dataFactoryType, f.ids[dataFactoryType]
			}
			request := f.request(kind)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(err)
			}
			request.KnownNativeIDs = []string{id}
			request.KnownNativeMetadata = map[string]map[string]any{id: batch.Items[0].Normalized}
			f.omitted[id] = true
			switch mode {
			case "gone", "surviving-child":
				delete(f.resources, id)
			case "forbidden":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return nil, false
				}
			case "forged-binding":
				request.KnownNativeMetadata[id][dataFactoryProof] = "forged"
			case "foreign-connection":
				request.KnownNativeMetadata[id]["_datafactory_connection"] = "other"
			case "missing-metadata":
				request.KnownNativeMetadata = nil
			case "unrequested-metadata":
				request.KnownNativeMetadata[id+"2"] = map[string]any{}
			case "duplicate-id":
				request.KnownNativeIDs = append(request.KnownNativeIDs, id)
			}
			result, err := f.runtime.List(t.Context(), request)
			if mode == "omitted" {
				if err != nil || !result.Complete || len(result.Items) != 1 || len(result.AbsentNativeIDs) != 0 {
					t.Fatal("live omission marked absent", err)
				}
				return
			}
			if mode == "gone" {
				if err != nil || !result.Complete || len(result.Items) != 0 || !slices.Equal(result.AbsentNativeIDs, []string{id}) {
					t.Fatal("own absence was not emitted", err, result.AbsentNativeIDs)
				}
				return
			}
			if err == nil || result.Complete {
				t.Fatal("invalid known observation completed", mode)
			}
		})
	}
}

func TestDataFactoryInventoryScopeProtectionAndCursor(t *testing.T) {
	for _, mode := range []string{"region-match", "region-miss", "global", "wrong-source", "group-lock", "protected-tag", "managed-group", "factory-creation", "node-creation", "runtime-creation", "unknown-runtime", "unknown-cdc", "unknown-trigger", "cursor", "cursor-parent-drift", "cursor-private-drift", "cursor-scope-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			kind := dataFactoryDatasetType
			request := f.request(kind)
			reason := ""
			switch mode {
			case "region-match", "region-miss":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
				if mode == "region-miss" {
					request.Scope.NativeID = "westus"
				}
			case "global":
				request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"}
			case "wrong-source":
				request.Source = productInventorySource
			case "group-lock":
				f.locks = []any{map[string]any{"id": text(f.group["id"]) + "/providers/Microsoft.Authorization/locks/protected", "properties": map[string]any{"level": "CanNotDelete"}}}
				reason = "azure_management_lock"
			case "protected-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
				reason = "azure_protected_tag"
			case "managed-group":
				f.group["managedBy"] = strings.ToLower(resourceID("Microsoft.Solutions/applications", "managed"))
				reason = "azure_managed_resource_group"
			case "factory-creation":
				delete(object(f.resources[f.ids[dataFactoryType]]["properties"]), "createTime")
				reason = "azure_datafactory_creation_unverified"
			case "node-creation":
				kind = dataFactoryNodeType
				delete(f.resources[f.ids[kind]], "registerTime")
				reason = "azure_datafactory_node_creation_unverified"
			case "runtime-creation":
				kind = dataFactoryNodeType
				delete(object(object(f.statuses[f.ids[dataFactoryIRType]]["properties"])["typeProperties"]), "createTime")
				reason = "azure_datafactory_runtime_creation_unverified"
			case "unknown-runtime":
				kind = dataFactoryIRType
				object(f.statuses[f.ids[kind]]["properties"])["state"] = "FutureState"
				reason = "azure_datafactory_runtime_state_unverified"
			case "unknown-cdc":
				kind = dataFactoryCDCType
				reason = "azure_datafactory_cdc_state_unverified"
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(req.URL.Path), "/adfcdcs/"+last(f.ids[kind])+"/status") {
						return jsonResponse(200, "Unknown", nil), true
					}
					return nil, false
				}
			case "unknown-trigger":
				kind = dataFactoryTriggerType
				object(f.resources[f.ids[kind]]["properties"])["runtimeState"] = "FutureState"
				reason = "azure_datafactory_trigger_state_unverified"
			}
			if kind != dataFactoryDatasetType {
				request = f.request(kind)
			}
			if strings.HasPrefix(mode, "cursor") {
				id := f.ids[kind] + "2"
				raw := batchClone(f.resources[f.ids[kind]])
				raw["id"], raw["name"] = id, last(id)
				f.resources[id], f.kinds[id] = raw, kind
				request.Limit = 1
			}
			batch, err := f.runtime.List(t.Context(), request)
			if mode == "global" || mode == "wrong-source" {
				if err == nil || batch.Complete {
					t.Fatal("wrong source or scope accepted")
				}
				return
			}
			if mode == "region-miss" {
				if err != nil || !batch.Complete || len(batch.Items) != 0 {
					t.Fatal("factory region filter failed", err)
				}
				return
			}
			if err != nil || len(batch.Items) != 1 {
				t.Fatal("native scoped source failed", err)
			}
			if reason != "" {
				item := batch.Items[0]
				if item.Actionable == nil || *item.Actionable || item.Normalized["cleanup_protection_reason"] != reason {
					t.Fatal("native protection lost", reason, item.Normalized["cleanup_protection_reason"])
				}
				return
			}
			if !strings.HasPrefix(mode, "cursor") {
				return
			}
			if batch.Complete || batch.NextCursor == "" {
				t.Fatal("first bounded page has no cursor")
			}
			request.Cursor = batch.NextCursor
			switch mode {
			case "cursor-parent-drift":
				f.resources[f.ids[dataFactoryType]]["futurePrivateSetting"] = "changed"
			case "cursor-private-drift":
				f.resources[f.ids[kind]+"2"]["futurePrivateSetting"] = "changed"
			case "cursor-scope-drift":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			}
			after, err := f.runtime.List(t.Context(), request)
			if mode == "cursor" {
				if err != nil || !after.Complete || len(after.Items) != 1 || after.Items[0].NativeID == batch.Items[0].NativeID {
					t.Fatal("stable cursor failed", err)
				}
			} else if err == nil || after.Complete {
				t.Fatal("cursor survived changed private context")
			}
		})
	}
}
