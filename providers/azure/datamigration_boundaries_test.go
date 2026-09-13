package azure

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDataMigrationInventorySourceAndCursor(t *testing.T) {
	for _, mode := range []string{"pages", "region", "legacy", "source", "kind", "scope", "subscription", "options", "cursor-body", "cursor-oversized", "cursor-offset", "cursor-extra", "cursor-kind", "cursor-region", "cursor-connection", "cursor-private", "cursor-nodes", "cursor-new-resource"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			request := f.request(dataMigrationType)
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if err != nil || first.Complete || len(first.Items) != 1 || first.NextCursor == "" {
				t.Fatal("native first page", err, first)
			}
			request.Cursor = first.NextCursor
			switch mode {
			case "region":
				request.Cursor = ""
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
				request.Limit = 100
			case "legacy":
				request.Source = inventorySource
			case "source":
				request.Source = productInventorySource
			case "kind":
				request.ResourceKind = nil
			case "scope":
				request.Scope.Kind = asset.ScopeResourceGroup
			case "subscription":
				request.Scope.NativeID = "11111111-1111-1111-1111-111111111111"
			case "options":
				request.Options = map[string]any{"filter": "running"}
			case "cursor-body":
				request.Cursor = "not-base64"
			case "cursor-oversized":
				request.Cursor = strings.Repeat("A", 129<<10)
			case "cursor-offset", "cursor-extra":
				payload, _ := base64.RawURLEncoding.DecodeString(request.Cursor)
				var cursor productCursor
				if json.Unmarshal(payload, &cursor) != nil {
					t.Fatal("invalid cursor")
				}
				if mode == "cursor-offset" {
					cursor.Target = 999
				} else {
					cursor.Next = "https://example.invalid"
				}
				payload, _ = json.Marshal(cursor)
				request.Cursor = base64.RawURLEncoding.EncodeToString(payload)
			case "cursor-kind":
				kind := f.runtime.resourceKind(dataMigrationSQLServiceType)
				request.ResourceKind = &kind
			case "cursor-region":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			case "cursor-connection":
				request.ConnectionID = "other"
			case "cursor-private":
				f.resources[f.ids["SqlMi"]]["futurePrivateSetting"] = "changed"
			case "cursor-nodes":
				object(array(f.nodes[f.ids[dataMigrationSQLServiceType]]["nodes"])[0])["futurePrivateSetting"] = "changed"
			case "cursor-new-resource":
				id := f.ids["SqlMi"] + "new"
				raw := batchClone(f.resources[f.ids["SqlMi"]])
				raw["id"], raw["name"] = id, last(id)
				f.resources[id], f.kinds[id] = raw, dataMigrationType
			}
			var batch contracts.InventoryBatch
			if mode == "cursor-connection" {
				batch, err = f.runtime.listDataMigration(t.Context(), f.client, request)
			} else {
				batch, err = f.runtime.List(t.Context(), request)
			}
			switch mode {
			case "legacy":
				if err != nil || !batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("generic ARM source claimed DMS membership", err)
				}
			case "region":
				if err != nil || !batch.Complete || len(batch.Items) != 3 {
					t.Fatal("migration execution region not used", len(batch.Items), err)
				}
			case "pages":
				seen := map[string]bool{first.Items[0].NativeID: true}
				for i := 0; i < 5; i++ {
					if err != nil || len(batch.Items) != 1 || seen[batch.Items[0].NativeID] {
						t.Fatal("unstable native cursor", err, batch)
					}
					seen[batch.Items[0].NativeID] = true
					if batch.Complete {
						break
					}
					request.Cursor = batch.NextCursor
					batch, err = f.runtime.List(t.Context(), request)
				}
				if len(seen) != 5 || !batch.Complete || batch.NextCursor != "" {
					t.Fatal("native cursor lost a target migration", len(seen))
				}
			default:
				if err == nil || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("changed source/cursor accepted", mode, err)
				}
			}
		})
	}
}

func TestDataMigrationNativePagination(t *testing.T) {
	for _, kind := range []string{dataMigrationServiceType, dataMigrationProjectType, dataMigrationTaskType, dataMigrationFileType, dataMigrationServiceTaskType, dataMigrationSQLServiceType, dataMigrationMongoServiceType, dataMigrationType} {
		for _, mode := range []string{"valid", "duplicate", "filtered", "version", "foreign", "cycle", "missing-value", "empty-token", "duplicate-token"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newDataMigrationFixture(t)
				id := f.ids[kind]
				collection := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + kind)
				if parent := dataMigrationParent(id, kind); parent != "" {
					collection = parent + "/" + strings.ToLower(last(kind))
				}
				rows := []any{f.resources[id]}
				if kind == dataMigrationType {
					collection = f.ids[dataMigrationSQLServiceType] + "/listmigrations"
					rows = []any{f.resources[f.ids["SqlDb"]], f.resources[f.ids["SqlMi"]], f.resources[f.ids["SqlVm"]]}
				}
				pages := 0
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method != "GET" || strings.ToLower(req.URL.Path) != collection {
						return nil, false
					}
					next := apiURL(collection, dataMigrationVersion) + "&$skiptoken=page-two"
					if req.URL.Query().Get("$skiptoken") != "" {
						pages++
						body := map[string]any{"value": rows}
						if mode == "cycle" {
							body["nextLink"] = next
						}
						if mode == "missing-value" {
							delete(body, "value")
						}
						return jsonResponse(200, body, nil), true
					}
					body := map[string]any{"value": []any{}, "nextLink": next}
					switch mode {
					case "duplicate":
						body["value"] = rows
					case "filtered":
						body["nextLink"] = next + "&$filter=state"
					case "version":
						body["nextLink"] = strings.Replace(next, dataMigrationVersion, "2021-06-30", 1)
					case "foreign":
						body["nextLink"] = strings.Replace(next, "management.azure.com", "example.invalid", 1)
					case "empty-token":
						body["nextLink"] = strings.TrimSuffix(next, "page-two")
					case "duplicate-token":
						body["nextLink"] = next + "&$skiptoken=page-three"
					}
					return jsonResponse(200, body, nil), true
				}
				batch, err := f.list(t, kind)
				if mode == "valid" {
					if err != nil || !batch.Complete || len(batch.Items) == 0 || pages < 2 {
						t.Fatal("native pagination incomplete", err, pages)
					}
				} else if err == nil || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("incomplete native pagination accepted", mode, err)
				}
			})
		}
	}
}

func TestDataMigrationProtectionAndKnownBinding(t *testing.T) {
	for _, mode := range []string{"root-tag", "group-tag", "group-managed", "target-tag", "target-managed", "target-lock", "ancestor-lock", "service-busy", "task-unknown", "migration-unknown", "operation-missing", "operation-invalid", "proof", "known-connection", "known-extra", "known-duplicate", "known-foreign", "known-members", "known-kind"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			kind := dataMigrationType
			if strings.HasPrefix(mode, "task") || strings.HasPrefix(mode, "ancestor") {
				kind = dataMigrationTaskType
			}
			batch, err := f.list(t, kind)
			if err != nil {
				t.Fatal(err)
			}
			item := batch.Items[0]
			if kind == dataMigrationType {
				for _, candidate := range batch.Items {
					if candidate.NativeID == f.ids["SqlDb"] {
						item = candidate
					}
				}
			}
			request := f.request(kind, item)
			root := text(item.Normalized["_datamigration_service"])
			target, _ := dataMigrationTarget(item.NativeID)
			switch mode {
			case "root-tag":
				f.resources[root]["tags"] = map[string]any{"steward:protected": "true"}
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "group-managed":
				f.group["managedBy"] = resourceID("Microsoft.Solutions/applications", "controller")
			case "target-tag":
				f.resources[target]["tags"] = map[string]any{"steward:protected": "true"}
			case "target-managed":
				f.resources[target]["managedBy"] = resourceID("Microsoft.Solutions/applications", "controller")
			case "target-lock", "ancestor-lock":
				if mode == "ancestor-lock" {
					target = root
				}
				f.locks = []any{map[string]any{"id": target + "/providers/Microsoft.Authorization/locks/new", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "service-busy":
				object(f.resources[root]["properties"])["provisioningState"] = "Updating"
			case "task-unknown":
				object(f.resources[item.NativeID]["properties"])["state"] = "Unknown"
			case "migration-unknown":
				object(f.resources[item.NativeID]["properties"])["migrationStatus"] = "Unknown"
			case "operation-missing":
				delete(object(f.resources[item.NativeID]["properties"]), "migrationOperationId")
			case "operation-invalid":
				object(f.resources[item.NativeID]["properties"])["migrationOperationId"] = "not-a-uuid"
			case "proof":
				item.Normalized[dataMigrationProof] = "forged"
			case "known-connection":
				request.ConnectionID = "other"
			case "known-extra":
				request.KnownNativeMetadata[item.NativeID+"other"] = item.Normalized
			case "known-duplicate":
				request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
			case "known-foreign":
				request.KnownNativeIDs[0] = strings.Replace(item.NativeID, testSubscription, "11111111-1111-1111-1111-111111111111", 1)
			case "known-members":
				item.Normalized[dataMigrationMembers] = map[string]any{"forged": map[string]any{"kind": dataMigrationTaskType}}
			case "known-kind":
				k := f.runtime.resourceKind(dataMigrationSQLServiceType)
				request.ResourceKind = &k
			}
			if mode == "known-connection" {
				batch, err = f.runtime.listDataMigration(t.Context(), f.client, request)
			} else {
				batch, err = f.runtime.List(t.Context(), request)
			}
			if strings.HasPrefix(mode, "known-") || mode == "proof" {
				if err == nil || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("unbound known context accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal("protected inventory failed", mode, err)
			}
			found := false
			for _, value := range batch.Items {
				if value.NativeID != item.NativeID {
					continue
				}
				found = true
				if value.Actionable == nil || *value.Actionable || value.Normalized["cleanup_protected"] != true || value.Normalized["cleanup_protection_reason"] == "" {
					t.Fatal("native protection bypassed", mode)
				}
			}
			if !found {
				t.Fatal("protected migration disappeared")
			}
		})
	}
}

func TestDataMigrationRecordedResourceReads(t *testing.T) {
	resources, collections, absent := 0, 0, 0
	for _, row := range dataMigrationRecordings(t) {
		u, _ := url.Parse(row.URL)
		if row.Method != "GET" || strings.Contains(u.Path, "/locations/") {
			continue
		}
		c := directClient(func(req *http.Request) (*http.Response, error) { return row.response(), nil })
		c.subscription = strings.Split(u.Path, "/")[2]
		res, err := c.request(t.Context(), "GET", row.URL)
		if row.Status == 404 {
			if !isNotFound(err) {
				t.Fatal("native own absence lost", row.Index, err)
			}
			absent++
			continue
		}
		if err != nil {
			t.Fatal("native resource response rejected", row.Index, err)
		}
		bodies := []map[string]any{res.data}
		if rows, ok := res.data["value"].([]any); ok {
			bodies = nil
			for _, raw := range rows {
				bodies = append(bodies, object(raw))
			}
			collections++
		} else {
			resources++
		}
		for _, body := range bodies {
			id, typ, err := parseID(text(body["id"]))
			kind := dataMigrationKind(typ)
			if err != nil || c.dataMigrationIdentity(id, kind) != nil || dataMigrationMetadata(id, kind, body) != nil {
				t.Fatal("native resource identity rejected", row.Index, kind, err)
			}
			safe := object(dataMigrationSafeValue(body))
			encoded, _ := json.Marshal(safe)
			if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "ConnectionInfo") {
				t.Fatal("native private input leaked")
			}
			before := c.privateConfiguration(dataMigrationSnapshot(kind, body))
			props := object(body["properties"])
			if slices.Contains([]string{dataMigrationTaskType, dataMigrationServiceTaskType}, kind) {
				props["state"], props["output"], props["errors"] = "Canceled", []any{}, []any{}
				if before != c.privateConfiguration(dataMigrationSnapshot(kind, body)) {
					t.Fatal("native task completion invalidated configuration")
				}
			}
		}
	}
	if resources != 47 || collections != 5 || absent != 5 {
		t.Fatal("native read coverage changed", resources, collections, absent)
	}
}
