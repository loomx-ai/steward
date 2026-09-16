package azure

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestDataProtectionOfficialSourceDescriptors(t *testing.T) {
	for _, name := range []string{"GetBackupInstance.json", "GetBackupInstance_ADLSBlobBackupDatasourceParameters.json", "GetBackupInstance_ADLSBlobBackupAutoProtection.json", "GetBackupInstance_BlobBackupAutoProtection.json"} {
		t.Run(name, func(t *testing.T) {
			wire, err := os.ReadFile("fixtures/dataprotection/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var example map[string]any
			if err = json.Unmarshal(wire, &example); err != nil {
				t.Fatal(err)
			}
			body := object(object(object(example["responses"])["200"])["body"])
			refs, err := dataProtectionSourceReferences(body)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for kind, ids := range refs {
				for _, id := range ids {
					count++
					canonical, actual, err := parseID(id)
					if err != nil || canonical != id || !strings.EqualFold(kind, actual) {
						t.Fatal("source kind was inferred from workload label", refs)
					}
				}
			}
			want := 1
			if name == "GetBackupInstance.json" {
				want = 2
			}
			if count != want {
				t.Fatal("lost source/set or failed to deduplicate same source", refs)
			}
			// resourceUri and workload labels must not become alternate destinations.
			props := object(body["properties"])
			for _, field := range []string{"dataSourceInfo", "dataSourceSetInfo"} {
				descriptor := object(props[field])
				descriptor["resourceUri"] = "https://untrusted.invalid/credential"
				descriptor["resourceType"] = "workload-label"
				descriptor["datasourceType"] = "workload-label"
			}
			again, err := dataProtectionSourceReferences(body)
			a, _ := json.Marshal(refs)
			b, _ := json.Marshal(again)
			if err != nil || string(a) != string(b) {
				t.Fatal("descriptive fields changed ARM identity", err)
			}
		})
	}
}

func TestDataProtectionSourceDescriptorBoundaries(t *testing.T) {
	source := strings.ToLower(resourceID(diskType, "source"))
	for _, scenario := range []string{"missing", "nonobject", "missing id", "nonstring", "whitespace", "control", "traversal", "query", "encoded", "foreign subscription", "opaque", "opaque set", "malformed set", "duplicate", "different set"} {
		t.Run(scenario, func(t *testing.T) {
			raw := map[string]any{"properties": map[string]any{"dataSourceInfo": map[string]any{"resourceID": source}}}
			props := object(raw["properties"])
			descriptor := object(props["dataSourceInfo"])
			valid, count := false, 0
			switch scenario {
			case "missing":
				delete(props, "dataSourceInfo")
			case "nonobject":
				props["dataSourceInfo"] = "value"
			case "missing id":
				delete(descriptor, "resourceID")
			case "nonstring":
				descriptor["resourceID"] = 1
			case "whitespace":
				descriptor["resourceID"] = " " + source
			case "control":
				descriptor["resourceID"] = source + "\n"
			case "traversal":
				descriptor["resourceID"] = source + "/../other"
			case "query":
				descriptor["resourceID"] = source + "?api-version=other"
			case "encoded":
				descriptor["resourceID"] = source + "%2fother"
			case "foreign subscription":
				descriptor["resourceID"] = strings.Replace(source, testSubscription, "22222222-2222-4222-8222-222222222222", 1)
				valid, count = true, 1
			case "opaque":
				descriptor["resourceID"] = "fabric:non-azure/source"
				valid = true
			case "opaque set":
				props["dataSourceSetInfo"] = map[string]any{"resourceID": "fabric:non-azure/set"}
				valid, count = true, 1
			case "malformed set":
				props["dataSourceSetInfo"] = []any{}
			case "duplicate":
				props["dataSourceSetInfo"] = map[string]any{"resourceID": strings.ToUpper(source)}
				valid, count = true, 1
			case "different set":
				props["dataSourceSetInfo"] = map[string]any{"resourceID": strings.ToLower(resourceID("Microsoft.Storage/storageAccounts", "set"))}
				valid, count = true, 2
			}
			refs, err := dataProtectionSourceReferences(raw)
			if (err == nil) != valid {
				t.Fatal("descriptor acceptance", refs, err)
			}
			if valid {
				got := 0
				for _, ids := range refs {
					got += len(ids)
				}
				if got != count {
					t.Fatal(refs)
				}
			}
		})
	}
}

func protectionSourceAsset(f *protectionFixture) asset.Asset {
	return asset.Asset{ID: "source", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: diskType, NativeID: strings.ToLower(resourceID(diskType, "source"))}, Location: "eastus", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
}

func TestDataProtectionSourceGraphAndIndependentSelection(t *testing.T) {
	f := newProtectionInstanceFixture(t)
	backup := f.request(t).Asset
	backup.Capabilities = asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}
	source := protectionSourceAsset(f.protectionFixture)
	retained := backup
	retained.ID = "retained"
	retained.Identity.NativeType = dataProtectionDeletedInstance
	retained.Identity.NativeID = f.deletedInstance
	retained.Capabilities = asset.CapabilitySet{asset.CapabilityIndexed}
	values := []asset.Asset{backup, source, retained}
	wire, _ := json.Marshal(values)
	if err := json.Unmarshal(wire, &values); err != nil {
		t.Fatal(err)
	}
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
		if r.Source == "azure:backup-source-reference" {
			refs = append(refs, r)
		}
	}
	if len(refs) != 1 || refs[0].SourceAssetID != backup.ID || refs[0].TargetAssetID != source.ID || refs[0].Type != graph.RelationshipUses || len(contribution.Bindings) != 0 {
		t.Fatal("source ownership or retained-history dependency invented", contribution)
	}
	for _, selected := range [][]asset.AssetID{{backup.ID}, {source.ID}, {backup.ID, source.ID}} {
		solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: selected, Relationships: refs, LifecycleBindings: contribution.Bindings})
		if err != nil || len(solved.Blockers) != 0 || len(solved.ImpactItems) != 0 || len(solved.Steps) != len(selected) {
			t.Fatal("changed independent selection", solved, err)
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
				t.Fatal("source deletion did not follow selected backup", solved.Steps)
			}
		}
	}
}

func TestDataProtectionSourceGraphRejectsChangedNativeEvidence(t *testing.T) {
	for _, scenario := range []string{"source changed", "set added", "metadata changed", "proof changed", "foreign connection", "foreign partition", "foreign subscription", "wrong kind", "missing own", "forbidden", "partial", "parent missing", "ambiguous source"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProtectionInstanceFixture(t)
			backup := f.request(t).Asset
			source := protectionSourceAsset(f.protectionFixture)
			values := []asset.Asset{backup, source}
			props := object(f.objects[f.instance]["properties"])
			switch scenario {
			case "source changed":
				object(props["dataSourceInfo"])["resourceID"] = source.Identity.NativeID + "-new"
			case "set added":
				props["dataSourceSetInfo"] = map[string]any{"resourceID": source.Identity.NativeID + "-set"}
			case "metadata changed":
				props["futureConfig"] = "secret-configuration"
			case "proof changed":
				backup.Normalized["_data_protection_configuration"] = "forged"
			case "foreign connection":
				backup.Identity.ConnectionID = "other"
			case "foreign partition":
				backup.Identity.Partition = "other"
			case "foreign subscription":
				backup.Identity.NativeID = strings.Replace(backup.Identity.NativeID, testSubscription, "22222222-2222-4222-8222-222222222222", 1)
			case "wrong kind":
				backup.Identity.NativeType = dataProtectionDeletedInstance
			case "missing own":
				delete(f.objects, f.instance)
			case "forbidden", "partial", "parent missing":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.instance) {
						code := 403
						reason := "Forbidden"
						if scenario == "partial" {
							code = 202
						}
						if scenario == "parent missing" {
							code = 404
							reason = "ParentResourceNotFound"
						}
						return jsonResponse(code, map[string]any{"error": map[string]any{"code": reason}}, nil), true
					}
					return nil, false
				}
			case "ambiguous source":
				duplicate := source
				duplicate.ID = "source-duplicate"
				values = append(values, duplicate)
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.contributeDataProtectionSources(t.Context(), "connection", backup, values); err == nil {
				t.Fatal("uncertain graph accepted")
			}
		})
	}
}

func TestDataProtectionSourceGraphKeepsExternalReferencesUnresolved(t *testing.T) {
	for _, scenario := range []string{"unscanned", "foreign connection", "foreign partition", "foreign subscription", "unknown kind", "opaque"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProtectionInstanceFixture(t)
			source := protectionSourceAsset(f.protectionFixture)
			if scenario == "foreign subscription" {
				source.Identity.NativeID = strings.Replace(source.Identity.NativeID, testSubscription, "22222222-2222-4222-8222-222222222222", 1)
			}
			if scenario == "unknown kind" {
				source.Identity.NativeID = strings.ToLower(resourceID("Microsoft.Future/workloads", "source"))
				source.Identity.NativeType = "Microsoft.Future/workloads"
			}
			id := source.Identity.NativeID
			if scenario == "opaque" {
				id = "fabric:non-azure/source"
			}
			object(object(f.objects[f.instance]["properties"])["dataSourceInfo"])["resourceID"] = id
			backup := f.request(t).Asset
			values := []asset.Asset{backup}
			if scenario == "foreign connection" {
				source.Identity.ConnectionID = "other"
				values = append(values, source)
			}
			if scenario == "foreign partition" {
				source.Identity.Partition = "other"
				values = append(values, source)
			}
			if scenario == "foreign subscription" {
				values = append(values, source)
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			previous := f.calls
			contribution, err := c.contributeDataProtectionSources(t.Context(), "connection", backup, values)
			if err != nil || len(contribution.Relationships) != 0 || len(contribution.Bindings) != 0 || f.calls-previous != 1 {
				t.Fatal("followed external source or created ownership", contribution, err)
			}
			want := 1
			if scenario == "opaque" {
				want = 0
			}
			if len(contribution.Unresolved) != want {
				t.Fatal(contribution)
			}
			if want == 1 && (contribution.Unresolved[0].NativeID != id || contribution.Unresolved[0].BlocksCleanup) {
				t.Fatal("external source became a deletion prerequisite", contribution)
			}
		})
	}
}

func TestDataProtectionSourceGraphPersistsThroughNativeScan(t *testing.T) {
	f := newProtectionInstanceFixture(t)
	repository, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	source := protectionSourceAsset(f.protectionFixture)
	source.ScopeID = "root"
	source.ResourceKindID = f.runtime.resourceKind(diskType).ID
	source.FirstSeenAt = time.Now().UTC()
	source.LastSeenAt = source.FirstSeenAt
	if err := repository.PutAsset(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	values := azureNativeWorkerScan(t, f.runtime, dataProtectionSource, repository, registry, []string{dataProtectionVault, dataProtectionPolicy, dataProtectionInstance, dataProtectionDeletedInstance, dataProtectionDeletedVault}, false, true)
	relations, err := repository.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var backup asset.Asset
	count := 0
	for _, value := range values {
		if value.Identity.NativeID == f.instance {
			backup = value
		}
	}
	for _, relation := range relations {
		if relation.Source == "azure:backup-source-reference" {
			count++
			if relation.TargetAssetID != source.ID || relation.Type != graph.RelationshipUses {
				t.Fatal("source reference did not survive SQLite", relation)
			}
		}
	}
	if backup.ID == "" || count != 2 {
		t.Fatal("native scan lost main or sibling source", count)
	}
	bindings, err := repository.ListLifecycleBindingsByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		if b.ManagedAssetID == source.ID || b.ControllerAssetID == backup.ID {
			t.Fatal("graph invented source ownership", b)
		}
	}
	solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{backup.ID, source.ID}, Relationships: relations, LifecycleBindings: bindings})
	if err != nil || len(solved.Blockers) != 0 || len(solved.ImpactItems) != 0 || len(solved.Steps) != 2 {
		t.Fatal("persisted graph changed independent selection", solved, err)
	}
	if solved.Steps[0].AssetID != backup.ID || solved.Steps[1].AssetID != source.ID || !slices.Contains(solved.Steps[1].DependsOn, solved.Steps[0].ID) {
		t.Fatal("persisted source graph lost deletion order", solved.Steps)
	}
}
