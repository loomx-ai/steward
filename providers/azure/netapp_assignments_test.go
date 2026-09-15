package azure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func netappAssignmentFixture(t *testing.T, kind string) (*netappFixture, string, string) {
	t.Helper()
	f := newNetappFixture(t)
	account := strings.ToLower(resourceID(netappAccountType, "first"))
	id := account + "/" + strings.ToLower(last(kind)) + "/item"
	volume := account + "/capacitypools/item/volumes/item"
	for _, field := range netappAssignmentFields {
		if field.kind == kind {
			object(f.objects[volume]["properties"])["dataProtection"] = map[string]any{field.section: map[string]any{field.field: id, "policyEnforced": false}}
		}
	}
	if kind == netappAccountType+"/backupPolicies" {
		object(f.objects[id]["properties"])["volumeBackups"] = []any{map[string]any{"volumeResourceId": volume, "volumeName": "item", "policyEnabled": false}}
		object(f.objects[id]["properties"])["volumesAssigned"] = json.Number("1")
	}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, id+"/volumes") {
			if q.Method != "GET" || q.ContentLength != 0 {
				t.Fatal("snapshot policy consumer read sent body", q.Method, q.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{f.objects[volume]}}, nil), true
		}
		return fleetGraphEmptyIndexes(t, q)
	}
	return f, id, volume
}
func netappAssignmentItem(t *testing.T, f *netappFixture, kind string, known map[string]map[string]any) contracts.InventoryItem {
	t.Helper()
	req := netappRequest(f.runtime, kind)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	req.KnownNativeMetadata = known
	for id := range known {
		req.KnownNativeIDs = append(req.KnownNativeIDs, id)
	}
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.Items) != 1 || !page.Complete {
		t.Fatal("assignment inventory", err, len(page.Items))
	}
	return page.Items[0]
}
func TestNetappAssignmentInventoryAndKnownConsumers(t *testing.T) {
	for _, field := range netappAssignmentFields {
		t.Run(field.kind, func(t *testing.T) {
			f, id, volume := netappAssignmentFixture(t, field.kind)
			f.paged = true
			item := netappAssignmentItem(t, f, field.kind, nil)
			review := object(item.Normalized[netappAssignmentReview])
			consumers := object(review["consumers"])
			if len(consumers) != 1 || object(consumers[volume])["uid"] != object(f.objects[volume]["properties"])["fileSystemId"] || review["native_index_complete"] != true {
				t.Fatal("current disabled assignment lost", review)
			}
			wire, _ := json.Marshal(item)
			if strings.Contains(string(wire), "netapp-private-canary") || item.Actionable == nil || *item.Actionable != (field.kind == netappSnapshotPolicyType) {
				t.Fatal("unsafe assignment output", string(wire))
			}
			known := map[string]map[string]any{id: {netappAssignmentReview: review}}
			f.hidden[volume] = true
			f.hidden[redisParentID(volume)] = true
			again := netappAssignmentItem(t, f, field.kind, known)
			if len(object(object(again.Normalized[netappAssignmentReview])["consumers"])) != 1 {
				t.Fatal("known omitted assignment lost")
			}
			// Remove the real assignment and both optional native association indexes.
			object(object(f.objects[volume]["properties"])["dataProtection"])[field.section] = map[string]any{field.field: ""}
			object(f.objects[id]["properties"])["volumeBackups"] = []any{}
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, id+"/volumes") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				return nil, false
			}
			gone := netappAssignmentItem(t, f, field.kind, known)
			if len(object(object(gone.Normalized[netappAssignmentReview])["consumers"])) != 0 {
				t.Fatal("unassignment not observed")
			}
		})
	}
}
func TestNetappAssignmentsRejectIncompleteReads(t *testing.T) {
	for _, fault := range []string{"forbidden", "pool forbidden", "collection failure", "malformed protection", "malformed backup", "malformed snapshot", "malformed enforcement", "wrong kind", "foreign hint", "region", "identity", "duplicate", "changed", "stale native index", "filtered page"} {
		t.Run(fault, func(t *testing.T) {
			kind := netappAccountType + "/snapshotPolicies"
			f, id, volume := netappAssignmentFixture(t, kind)
			req := netappRequest(f.runtime, kind)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			prior := f.override
			reads := 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if path == volume {
					reads++
					if fault == "forbidden" {
						return jsonResponse(403, nil, nil), true
					}
					if fault == "changed" && reads > 1 {
						object(f.objects[volume]["properties"])["usageThreshold"] = json.Number("123456789")
					}
				}
				if fault == "pool forbidden" && path == redisParentID(volume) {
					return jsonResponse(403, nil, nil), true
				}
				if fault == "collection failure" && path == redisParentID(volume)+"/volumes" {
					return jsonResponse(503, nil, nil), true
				}
				if path == id+"/volumes" {
					if fault == "duplicate" {
						return jsonResponse(200, map[string]any{"value": []any{f.objects[volume], f.objects[volume]}}, nil), true
					}
					if fault == "filtered page" {
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(path, netappVersion) + "&$filter=name"}, nil), true
					}
				}
				return prior(q)
			}
			p := object(f.objects[volume]["properties"])
			switch fault {
			case "malformed protection":
				p["dataProtection"] = []any{}
			case "malformed backup":
				object(p["dataProtection"])["backup"] = "invalid"
			case "malformed snapshot":
				object(p["dataProtection"])["snapshot"] = false
			case "malformed enforcement":
				object(p["dataProtection"])["backup"] = map[string]any{"policyEnforced": "false"}
			case "wrong kind":
				object(object(p["dataProtection"])["snapshot"])["snapshotPolicyId"] = volume
			case "foreign hint":
				req.KnownNativeIDs = []string{id}
				req.KnownNativeMetadata = map[string]map[string]any{id: {netappAssignmentReview: map[string]any{"consumers": map[string]any{strings.Replace(volume, testSubscription, testApplication, 1): map[string]any{}}}}}
			case "region":
				f.objects[volume]["location"] = "westus"
			case "identity":
				p["fileSystemId"] = ""
			case "stale native index":
				p["dataProtection"] = map[string]any{}
			}
			page, err := f.runtime.List(t.Context(), req)
			if err == nil || page.Complete || len(page.AbsentNativeIDs) != 0 {
				t.Fatal("incomplete assignments accepted", fault, page, err)
			}
		})
	}
}
func TestNetappAssignmentsRemovedPoolAndHistoricalBackup(t *testing.T) {
	kind := netappAccountType + "/backupVaults"
	f, id, volume := netappAssignmentFixture(t, kind)
	item := netappAssignmentItem(t, f, kind, nil)
	known := map[string]map[string]any{id: {netappAssignmentReview: item.Normalized[netappAssignmentReview]}}
	f.missing[volume] = true
	f.missing[redisParentID(volume)] = true
	gone := netappAssignmentItem(t, f, kind, known)
	if len(object(object(gone.Normalized[netappAssignmentReview])["consumers"])) != 0 {
		t.Fatal("removed pool retained current assignment")
	}
	// No absent volume is returned by a vault scan; the retained backup is distinct.
	backup := id + "/backups/item"
	if f.objects[backup] == nil || f.missing[backup] {
		t.Fatal("backup removed with assignment")
	}
}
func TestNetappSnapshotPolicyNativeListExample(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/SnapshotPolicies_ListVolumes.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wire)
	if hex.EncodeToString(sum[:]) != "4c73870b60fe614d9fa6db6905be0813975e5d8366e34715206946aea8f9f572" {
		t.Fatal("pinned native example changed")
	}
	var fixture map[string]any
	if json.Unmarshal(wire, &fixture) != nil {
		t.Fatal("native JSON")
	}
	body := object(object(object(fixture["responses"])["200"])["body"])
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.NetApp.SnapshotPolicies_ListVolumes")
	if !ok {
		t.Fatal("native operation missing")
	}
	params := object(fixture["parameters"])
	if _, err := bindAzureREST(op, params); err == nil {
		t.Fatal("undeclared example body accepted")
	}
	delete(params, "body")
	bound, err := bindAzureREST(op, params)
	if err != nil || bound.Method != "GET" || bound.Body != nil {
		t.Fatal("native GET must not send example's extraneous body", bound, err)
	}
	rows := body["value"].([]any)
	if len(rows) != 1 {
		t.Fatal("native list body")
	}
	raw := object(rows[0])
	id := strings.ToLower(text(raw["id"]))
	if !netappMetadata(raw, id, netappVolumeType) {
		t.Fatal("native row validation")
	}
}

func TestNetappAssignmentWorkerGraphAndFailedScan(t *testing.T) {
	kind := netappAccountType + "/snapshotPolicies"
	f, id, volume := netappAssignmentFixture(t, kind)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	if len(values) != 22 {
		t.Fatal("complete native graph", len(values))
	}
	var parent, consumer asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == id {
			parent = v
		}
		if v.Identity.NativeID == volume {
			consumer = v
		}
	}
	relationships, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range relationships {
		if r.Evidence["current_assignment"] == true && r.SourceAssetID == consumer.ID && r.TargetAssetID == parent.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("missing current policy consumer relationship")
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing", "changed", "forged"} {
		t.Run(fault, func(t *testing.T) {
			p := parent
			p.Normalized = batchClone(parent.Normalized)
			v := consumer
			v.Normalized = batchClone(consumer.Normalized)
			members := []asset.Asset{v}
			switch fault {
			case "missing":
				members = nil
			case "changed":
				v.Normalized["_netapp_configuration"] = "changed"
			case "forged":
				p.Normalized[netappAssignmentProof] = "forged"
			}
			result, err := c.netappAssignmentContribution(p, members)
			if err != nil || len(result.Unresolved) == 0 || len(result.Bindings) != 0 {
				t.Fatal("stale assignment graph trusted", result, err)
			}
		})
	}
	prior := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, volume) {
			return jsonResponse(403, nil, nil), true
		}
		return prior(q)
	}
	after := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, []string{kind}, true, true)
	if len(after) != 22 {
		t.Fatal("failed association scan removed assets", len(after))
	}
	for _, v := range after {
		if v.ID == parent.ID && v.Normalized[netappAssignmentProof] != parent.Normalized[netappAssignmentProof] {
			t.Fatal("failed scan overwrote reviewed assignment")
		}
	}
}

func TestNetappBackupPolicyIncompleteNativeIndex(t *testing.T) {
	kind := netappAccountType + "/backupPolicies"
	for _, mode := range []string{"name only", "count mismatch", "invalid count", "invalid rows", "invalid id"} {
		t.Run(mode, func(t *testing.T) {
			f, id, _ := netappAssignmentFixture(t, kind)
			p := object(f.objects[id]["properties"])
			switch mode {
			case "name only":
				p["volumeBackups"] = []any{map[string]any{"volumeName": "item"}}
			case "count mismatch":
				p["volumesAssigned"] = json.Number("2")
			case "invalid count":
				p["volumesAssigned"] = "1"
			case "invalid rows":
				p["volumeBackups"] = map[string]any{}
			case "invalid id":
				p["volumeBackups"] = []any{map[string]any{"volumeResourceId": id}}
			}
			req := netappRequest(f.runtime, kind)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			page, err := f.runtime.List(t.Context(), req)
			if strings.HasPrefix(mode, "invalid") {
				if err == nil {
					t.Fatal("malformed native evidence accepted", mode)
				}
				return
			}
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			item := page.Items[0]
			review := object(item.Normalized[netappAssignmentReview])
			if review["native_index_complete"] != false || len(object(review["consumers"])) != 1 {
				t.Fatal("native incomplete index silently trusted", review)
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			parent := asset.Asset{ID: "policy", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: id, NativeType: kind}, Location: item.Location, Normalized: item.Normalized}
			contribution, err := c.netappAssignmentContribution(parent, nil)
			if err != nil || len(contribution.Unresolved) == 0 {
				t.Fatal("incomplete policy not blocked", contribution, err)
			}
		})
	}
}
