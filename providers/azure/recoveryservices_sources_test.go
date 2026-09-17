package azure

import (
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestRecoveryRecordedSourceReferences(t *testing.T) {
	for _, name := range []string{"hana-read-recording.json", "vm-item-delete-recording.json"} {
		t.Run(name, func(t *testing.T) {
			wire, err := os.ReadFile("fixtures/recoveryservices/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var rows []struct {
				Response struct{ Body struct{ String string } }
			}
			if err = json.Unmarshal(wire, &rows); err != nil {
				t.Fatal(err)
			}
			checked := 0
			for _, row := range rows {
				var raw map[string]any
				if json.Unmarshal([]byte(row.Response.Body.String), &raw) != nil {
					continue
				}
				values := array(raw["value"])
				if values == nil {
					values = []any{raw}
				}
				for _, v := range values {
					body := object(v)
					if object(body["properties"])["sourceResourceId"] == nil {
						continue
					}
					refs, err := recoveryServicesSourceReferences(body)
					if err != nil || len(refs) != 1 {
						t.Fatal("recorded source identity", err)
					}
					for kind, ids := range refs {
						if !strings.EqualFold(kind, "Microsoft.Compute/virtualMachines") || len(ids) != 1 {
							t.Fatal("lost or duplicated native VM reference")
						}
					}
					checked++
				}
			}
			if checked < 3 {
				t.Fatal("recorded source coverage missing", checked)
			}
		})
	}
}

func TestRecoverySourceReferenceBoundaries(t *testing.T) {
	id := strings.ToLower(resourceID("Microsoft.Compute/virtualMachines", "source"))
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"native", id, true}, {"absolute", "https://management.azure.com" + id, true},
		{"foreign-origin", "https://untrusted.invalid" + id, false}, {"port", "https://management.azure.com:443" + id, false},
		{"userinfo", "https://user@management.azure.com" + id, false}, {"query", "https://management.azure.com" + id + "?x=y", false},
		{"fragment", "https://management.azure.com" + id + "#fragment", false}, {"encoded", id + "%2fother", false},
		{"traversal", id + "/../other", false}, {"whitespace", " " + id, false}, {"control", id + "\n", false},
		{"number", 123, false}, {"object", map[string]any{}, false}, {"label", "hana-host", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := recoveryServicesSourceReferences(map[string]any{"properties": map[string]any{"sourceResourceId": tc.value}})
			if (err == nil) != tc.valid {
				t.Fatal("source identity acceptance", err)
			}
			if tc.valid && len(refs) != 1 {
				t.Fatal("missing source")
			}
		})
	}
	if _, err := recoveryServicesSourceReferences(map[string]any{"properties": map[string]any{"virtualMachineId": resourceID(diskType, "disk")}}); err == nil {
		t.Fatal("VM field accepted a disk")
	}
	refs, err := recoveryServicesSourceReferences(map[string]any{"properties": map[string]any{"friendlyName": "vm", "serverName": "host", "parentName": "hdb"}})
	if err != nil || len(refs) != 0 {
		t.Fatal("invented source from labels")
	}
}

func TestRecoverySourceGraphIndependentSelection(t *testing.T) {
	f := newRecoveryItemActionFixture(t)
	sourceID := strings.ToLower(resourceID("Microsoft.Compute/virtualMachines", "source"))
	f.objects[sourceID] = map[string]any{"id": sourceID, "name": "source", "type": "Microsoft.Compute/virtualMachines", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.Method == "GET" && strings.EqualFold(q.URL.Path, sourceID) {
			return jsonResponse(200, f.objects[sourceID], nil), true
		}
		if q.Method == "GET" && strings.EqualFold(q.URL.Path, sourceID+"/extensions") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return previous(q)
	}
	object(f.objects[f.item]["properties"])["sourceResourceId"] = sourceID
	backup := f.request(t).Asset
	backup.Capabilities = asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}
	source := asset.Asset{ID: "source", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: "Microsoft.Compute/virtualMachines", NativeID: sourceID}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
	values := []asset.Asset{backup, source}
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	var refs []graph.Relationship
	for _, r := range contribution.Relationships {
		if r.Source == "azure:recovery-source-reference" {
			refs = append(refs, r)
		}
	}
	if len(refs) != 1 || refs[0].SourceAssetID != backup.ID || refs[0].TargetAssetID != source.ID || len(contribution.Bindings) != 0 {
		t.Fatal("source ownership or missing association")
	}
	for _, selected := range [][]asset.AssetID{{backup.ID}, {source.ID}, {backup.ID, source.ID}} {
		solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: selected, Relationships: refs})
		if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != len(selected) || len(solved.ImpactItems) != 0 {
			t.Fatal("independent source selection changed", err)
		}
		if len(selected) == 2 {
			var first, second plan.CleanupTaskStep
			for _, step := range solved.Steps {
				if step.AssetID == backup.ID {
					first = step
				}
				if step.AssetID == source.ID {
					second = step
				}
			}
			if !slices.Contains(second.DependsOn, first.ID) || len(first.DependsOn) != 0 {
				t.Fatal("source scheduled before selected backup")
			}
		}
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	object(f.objects[f.item]["properties"])["sourceResourceId"] = sourceID + "-replacement"
	if _, err = c.contributeRecoverySources(t.Context(), "connection", backup, values); err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestRecoverySourceGraphUnresolvedAndRetained(t *testing.T) {
	for _, mode := range []string{"unscanned", "foreign-subscription", "retained", "duplicate", "foreign-connection", "missing-own", "forged-retention"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryItemActionFixture(t)
			id := strings.ToLower(resourceID("Microsoft.Compute/virtualMachines", "source"))
			if mode == "foreign-subscription" {
				id = strings.Replace(id, testSubscription, "22222222-2222-4222-8222-222222222222", 1)
			}
			object(f.objects[f.item]["properties"])["sourceResourceId"] = id
			backup := f.request(t).Asset
			source := asset.Asset{ID: "source", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: "Microsoft.Compute/virtualMachines", NativeID: id}}
			values := []asset.Asset{backup}
			if mode == "duplicate" {
				values = append(values, source, source)
			}
			if mode == "foreign-subscription" {
				values = append(values, source)
			}
			if mode == "retained" || mode == "forged-retention" {
				backup.Normalized["retained"] = true
			}
			if mode == "foreign-connection" {
				backup.Identity.ConnectionID = "other"
			}
			if mode == "missing-own" {
				delete(f.objects, f.item)
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "retained" {
				f.retain()
				backup.Normalized["_recovery_services_configuration"] = c.privateConfiguration(f.objects[f.item])
			}
			got, err := c.contributeRecoverySources(t.Context(), "connection", backup, values)
			if mode == "duplicate" || mode == "foreign-connection" || mode == "missing-own" || mode == "forged-retention" {
				if err == nil {
					t.Fatal("invalid graph accepted")
				}
				return
			}
			want := 1
			if mode == "retained" {
				want = 0
			}
			if err != nil || len(got.Unresolved) != want || len(got.Relationships) != 0 || len(got.Bindings) != 0 {
				t.Fatal("unresolved or retained source changed", err)
			}
		})
	}
}

func TestRecoveryContainerSourceGraph(t *testing.T) {
	for _, kind := range []string{vmType, storageType} {
		t.Run(kind, func(t *testing.T) {
			f := newRecoveryItemActionFixture(t)
			id := strings.ToLower(resourceID(kind, "source"))
			object(f.objects[f.container]["properties"])["sourceResourceId"] = "https://management.azure.com" + id
			if kind == storageType {
				object(f.objects[f.container]["properties"])["containerType"] = "StorageContainer"
			}
			batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f.recoveryServicesFixture, recoveryServicesContainer))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal("native container inventory", err)
			}
			item := batch.Items[0]
			parent := asset.Asset{ID: "container", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: recoveryServicesContainer, NativeID: item.NativeID}, Normalized: item.Normalized}
			source := asset.Asset{ID: "source", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: kind, NativeID: id}}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.contributeRecoverySources(t.Context(), "connection", parent, []asset.Asset{parent, source})
			if err != nil || len(got.Relationships) != 1 || len(got.Bindings) != 0 || len(got.Unresolved) != 0 || got.Relationships[0].TargetAssetID != source.ID {
				t.Fatal("container/source association", err)
			}
			object(f.objects[f.container]["properties"])["sourceResourceId"] = id + "-replacement"
			if _, err = c.contributeRecoverySources(t.Context(), "connection", parent, []asset.Asset{parent, source}); err == nil {
				t.Fatal("changed container source accepted")
			}
		})
	}
}

func TestRecoverySourceAliasesMustAgree(t *testing.T) {
	id := strings.ToLower(resourceID(vmType, "source"))
	props := map[string]any{"sourceResourceId": id, "virtualMachineId": strings.ToUpper(id)}
	refs, err := recoveryServicesSourceReferences(map[string]any{"properties": props})
	if err != nil || len(refs) != 1 || len(refs[vmType]) != 1 {
		t.Fatal("same native source was duplicated", err)
	}
	props["virtualMachineId"] = id + "-other"
	if _, err = recoveryServicesSourceReferences(map[string]any{"properties": props}); err == nil {
		t.Fatal("contradictory source fields accepted")
	}
}

func TestRecoverySoftDeletedContainerSourceIsHistorical(t *testing.T) {
	// The native retained state is registrationStatus, not the protected-item flag.
	example := recoveryServicesExample(t, "SoftDeletedContainers_List")
	rows := array(example["value"])
	if len(rows) != 1 {
		t.Fatal("missing official retained-container example")
	}
	raw := object(rows[0])
	props := object(raw["properties"])
	if props["registrationStatus"] != "SoftDeleted" || props["isScheduledForDeferredDelete"] != nil {
		t.Fatal("native retained-container shape changed")
	}
	refs, err := recoveryServicesSourceReferences(raw)
	if err != nil || len(refs[storageType]) != 1 {
		t.Fatal("native storage source lost", err)
	}
	f := newRecoveryItemActionFixture(t)
	// Scope adaptation is synthetic; the upstream fixture above remains unchanged.
	container := batchClone(raw)
	container["id"], container["name"] = f.container, last(f.container)
	f.objects[f.container] = container
	batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f.recoveryServicesFixture, recoveryServicesContainer))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal("retained container inventory", err)
	}
	item := batch.Items[0]
	parent := asset.Asset{ID: "retained-container", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: recoveryServicesContainer, NativeID: item.NativeID}, Normalized: item.Normalized}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.contributeRecoverySources(t.Context(), "connection", parent, []asset.Asset{parent})
	if err != nil || len(got.Relationships)+len(got.Unresolved)+len(got.Bindings) != 0 {
		t.Fatal("retained container acquired live source dependency", err)
	}
	parent.Normalized["state"] = "Registered"
	if _, err = c.contributeRecoverySources(t.Context(), "connection", parent, []asset.Asset{parent}); err == nil {
		t.Fatal("forged container retention accepted")
	}
}
