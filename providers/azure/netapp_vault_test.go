package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestNetappVaultInventoryRetainsKnownBackupHints(t *testing.T) {
	f, id, _ := netappAssignmentFixture(t, netappVaultType)
	f.paged = true
	item := netappAssignmentItem(t, f, netappVaultType, nil)
	backup := id + "/backups/item"
	review := object(item.Normalized[netappVaultReview])
	if len(object(review["members"])) != 1 || object(object(review["members"])[backup])["uid"] != object(f.objects[backup]["properties"])["backupId"] || item.Actionable == nil || !*item.Actionable {
		t.Fatal("vault membership or action boundary", review)
	}
	wire, _ := json.Marshal(item)
	if strings.Contains(string(wire), "netapp-private-canary") {
		t.Fatal("private backup fields exposed")
	}
	known := map[string]map[string]any{id: item.Normalized}
	f.hidden[backup] = true
	again := netappAssignmentItem(t, f, netappVaultType, known)
	if len(object(object(again.Normalized[netappVaultReview])["members"])) != 1 {
		t.Fatal("omitted own-live backup lost")
	}
	f.missing[backup] = true
	gone := netappAssignmentItem(t, f, netappVaultType, known)
	if len(object(object(gone.Normalized[netappVaultReview])["members"])) != 0 {
		t.Fatal("own-absent backup association kept")
	}
	// Vault membership discovery does not reconcile the backup resource itself.
	req := netappRequest(f.runtime, netappVaultType)
	req.KnownNativeMetadata = known
	req.KnownNativeIDs = []string{id}
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.AbsentNativeIDs) != 0 {
		t.Fatal("vault scan closed a backup", page, err)
	}
}

func TestNetappVaultIncompleteAndConcurrentReads(t *testing.T) {
	for _, fault := range []string{"backup forbidden", "collection unavailable", "vault absent", "wrong source", "wrong region", "foreign member", "duplicate", "filtered page", "listed absent", "changed backup", "changed vault", "late backup", "recreated omitted", "foreign hint"} {
		t.Run(fault, func(t *testing.T) {
			f, id, _ := netappAssignmentFixture(t, netappVaultType)
			item := netappAssignmentItem(t, f, netappVaultType, nil)
			backup := id + "/backups/item"
			req := netappRequest(f.runtime, netappVaultType)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			req.KnownNativeMetadata = map[string]map[string]any{id: item.Normalized}
			req.KnownNativeIDs = []string{id}
			prior := f.override
			reads, lists := 0, 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if path == backup {
					reads++
					switch fault {
					case "vault absent":
						f.missing[id] = true
					case "backup forbidden":
						return jsonResponse(403, nil, nil), true
					case "listed absent":
						return jsonResponse(404, nil, nil), true
					case "changed backup":
						if reads > 1 {
							object(f.objects[backup]["properties"])["backupId"] = testTenant
						}
					case "changed vault":
						f.objects[id]["etag"] = "changed-after-vault-read"
					case "recreated omitted":
						if reads == 1 {
							return jsonResponse(404, nil, nil), true
						}
						object(f.objects[backup]["properties"])["backupId"] = testTenant
					}
				}
				if path == id+"/backups" {
					lists++
					switch fault {
					case "collection unavailable":
						return jsonResponse(503, nil, nil), true
					case "duplicate":
						return jsonResponse(200, map[string]any{"value": []any{f.objects[backup], f.objects[backup]}}, nil), true
					case "foreign member":
						raw := batchClone(f.objects[backup])
						raw["id"] = strings.Replace(backup, testSubscription, testTenant, 1)
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
					case "filtered page":
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(path, netappVersion) + "&$filter=name"}, nil), true
					case "late backup":
						if lists == 1 {
							return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
						}
					}
				}
				return prior(q)
			}
			switch fault {
			case "wrong source":
				object(f.objects[backup]["properties"])["volumeResourceId"] = id
			case "wrong region":
				f.objects[backup]["location"] = "westus"
			case "recreated omitted":
				f.hidden[backup] = true
			case "late backup":
				delete(req.KnownNativeMetadata, id)
			case "foreign hint":
				req.KnownNativeMetadata = map[string]map[string]any{id: {netappVaultReview: map[string]any{"members": map[string]any{strings.Replace(backup, testSubscription, testTenant, 1): map[string]any{}}}}}
			}
			page, err := f.runtime.List(t.Context(), req)
			if err == nil || page.Complete || len(page.AbsentNativeIDs) != 0 {
				t.Fatal("incomplete vault observation accepted", fault, page, err)
			}
		})
	}
}

func TestNetappVaultWorkerGraphAndFailedRescan(t *testing.T) {
	f, id, _ := netappAssignmentFixture(t, netappVaultType)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	if len(values) != 22 {
		t.Fatal("full graph", len(values))
	}
	var vault, backup asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == id {
			vault = v
		}
		if v.Identity.NativeID == id+"/backups/item" {
			backup = v
		}
	}
	rows, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.SourceAssetID == backup.ID && row.TargetAssetID == vault.ID && row.Evidence["native_membership"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("missing native vault membership")
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing", "changed", "forged", "legacy", "changed assignments", "extra", "duplicate"} {
		t.Run(fault, func(t *testing.T) {
			p, b := vault, backup
			p.Normalized = batchClone(vault.Normalized)
			b.Normalized = batchClone(backup.Normalized)
			members := []asset.Asset{b}
			switch fault {
			case "missing":
				members = nil
			case "changed":
				members[0].Normalized["_netapp_configuration"] = "changed"
			case "forged":
				p.Normalized[netappVaultProof] = "forged"
			case "legacy":
				delete(p.Normalized, netappVaultReview)
			case "changed assignments":
				object(p.Normalized[netappAssignmentReview])["consumers"] = map[string]any{}
			case "extra":
				extra := b
				extra.ID = "extra"
				extra.Identity.NativeID += "-new"
				members = append(members, extra)
			case "duplicate":
				members = append(members, b)
			}
			out, err := c.netappVaultContribution(p, members)
			if fault == "duplicate" {
				if err == nil {
					t.Fatal("ambiguous member trusted")
				}
				return
			}
			if err != nil || len(out.Unresolved) == 0 || fault != "extra" && len(out.Bindings) != 0 {
				t.Fatal("stale vault graph trusted", out, err)
			}
		})
	}
	prior := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, id+"/backups/item") {
			return jsonResponse(403, nil, nil), true
		}
		return prior(q)
	}
	after := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, []string{netappVaultType}, true, true)
	if len(after) != len(values) {
		t.Fatal("failed vault scan removed assets", len(after))
	}
}
