package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func (f *dataFactoryFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	var values []asset.Asset
	for _, kind := range append(dataFactoryChildKinds(dataFactoryType), dataFactoryType, dataFactoryNodeType, dataFactoryEndpointType) {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil {
			t.Fatal("Data Factory lifecycle inventory", kind, err)
		}
		for _, item := range batch.Items {
			value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: item.NativeID}, Location: item.Location, Name: item.Name, Normalized: item.Normalized}
			if item.Actionable != nil && *item.Actionable {
				value.Capabilities = asset.CapabilitySet{asset.CapabilityActionable}
			}
			values = append(values, value)
		}
	}
	payload, _ := json.Marshal(values)
	if err := json.Unmarshal(payload, &values); err != nil {
		t.Fatal(err)
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if previous != nil {
			if res, ok := previous(req); ok {
				return res, true
			}
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	return values
}

func TestDataFactoryNativeLifecycleAndPlans(t *testing.T) {
	f := newDataFactoryFixture(t)
	values := f.assets(t)
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil || len(contribution.Bindings) != 13 {
		t.Fatal("Data Factory native lifecycle", err, len(contribution.Bindings), contribution.Unresolved)
	}
	for _, unresolved := range contribution.Unresolved {
		if unresolved.Relationship == graph.RelationshipAttachedTo || unresolved.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true {
			t.Fatal("native owned member or consumer missing", unresolved)
		}
	}
	for _, binding := range contribution.Bindings {
		kind := f.kinds[string(binding.ManagedAssetID)]
		if binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.DirectCleanupAllowed != (kind != dataFactoryNetworkType) || binding.Evidence["retention_supported"] != false {
			t.Fatal("native child authority changed", binding)
		}
		direct := kind == dataFactoryCDCType || kind == dataFactoryTriggerType
		if (binding.CleanupPolicy == graph.CleanupDirect) != direct || !direct && binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
			t.Fatal("preparation prerequisite or cascade proof missing", binding)
		}
	}
	for _, test := range []struct {
		kind           string
		steps, impacts int
		blocked        bool
	}{
		{dataFactoryType, 3, 11, false},
		{dataFactoryIRType, 1, 1, false},
		{dataFactoryNodeType, 1, 0, false},
		{dataFactoryEndpointType, 1, 0, false},
		{dataFactoryCDCType, 1, 0, false},
		{dataFactoryTriggerType, 1, 0, false},
		{dataFactoryPipelineType, 1, 0, true},
		{dataFactoryDatasetType, 1, 0, true},
	} {
		t.Run(test.kind, func(t *testing.T) {
			result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(f.ids[test.kind])}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
			if err != nil || len(result.Steps) != test.steps || len(result.ImpactItems) != test.impacts || (len(result.Blockers) != 0) != test.blocked {
				t.Fatal("native plan changed", err, len(result.Steps), len(result.ImpactItems), result.Blockers)
			}
		})
	}
	for _, mode := range []string{"retained", "protected"} {
		t.Run(mode, func(t *testing.T) {
			input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(f.ids[dataFactoryType])}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
			child := asset.AssetID(f.ids[dataFactoryDatasetType])
			if mode == "retained" {
				input.RequestOptions = map[asset.AssetID]map[string]any{asset.AssetID(f.ids[dataFactoryType]): {"retain_resources": []string{string(child)}}}
			} else {
				input.Protections = []plan.ProtectionPolicy{{AssetID: child, Protected: true}}
			}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) == 0 {
				t.Fatal("factory cascade bypassed retained or protected artifact", err)
			}
		})
	}
}

func TestDataFactoryFactoryCascadeHandlesArtifactCycles(t *testing.T) {
	f := newDataFactoryFixture(t)
	first := f.ids[dataFactoryPipelineType]
	second := strings.TrimSuffix(first, "/"+last(first)) + "/second-pipeline"
	raw := batchClone(f.resources[first])
	raw["id"], raw["name"] = second, last(second)
	f.resources[second], f.kinds[second] = raw, dataFactoryPipelineType
	for source, target := range map[string]string{first: second, second: first} {
		object(f.resources[source]["properties"])["activities"] = []any{map[string]any{"name": "invoke", "type": "ExecutePipeline", "typeProperties": map[string]any{"pipeline": map[string]any{"type": "PipelineReference", "referenceName": last(target)}}}}
	}
	values := f.assets(t)
	lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"factory", "artifacts-only", "retained"} {
		input := plan.Input{Assets: values, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(f.ids[dataFactoryType])}}
		if mode == "artifacts-only" {
			input.ResolvedAssetIDs = []asset.AssetID{asset.AssetID(first), asset.AssetID(second), asset.AssetID(f.ids[dataFactoryTriggerType])}
		}
		if mode == "retained" {
			input.RequestOptions = map[asset.AssetID]map[string]any{asset.AssetID(f.ids[dataFactoryType]): {"retain_resources": []string{second}}}
		}
		result, err := plan.Solve(input)
		if err != nil || (len(result.Blockers) == 0) != (mode == "factory") {
			t.Fatal("artifact cycle escaped its reviewed factory cascade", mode, err, result.Blockers)
		}
	}
}

func TestDataFactorySSISConsumersAreDirectPrerequisites(t *testing.T) {
	f := newDataFactoryFixture(t)
	id := f.ids[dataFactoryIRType]
	object(f.resources[id]["properties"])["type"] = "Managed"
	object(f.resources[id]["properties"])["typeProperties"] = map[string]any{"ssisProperties": map[string]any{"edition": "Standard"}}
	object(f.statuses[id]["properties"])["type"], object(f.statuses[id]["properties"])["state"] = "Managed", "Started"
	delete(f.resources, f.ids[dataFactoryNodeType])
	object(f.resources[f.ids[dataFactoryLinkedType]]["properties"])["connectVia"] = map[string]any{"type": "IntegrationRuntimeReference", "referenceName": last(id)}
	values := f.assets(t)
	lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	direct := map[string]bool{}
	for _, binding := range contribution.Bindings {
		if binding.CleanupPolicy == graph.CleanupDirect {
			direct[f.kinds[string(binding.ManagedAssetID)]] = true
		}
	}
	for _, kind := range []string{dataFactoryIRType, dataFactoryLinkedType, dataFactoryDatasetType, dataFactoryPipelineType, dataFactoryTriggerType, dataFactoryCDCType} {
		if !direct[kind] {
			t.Fatal("SSIS cleanup lost a direct consumer prerequisite", kind, direct)
		}
	}
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(f.ids[dataFactoryType])}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) != 0 {
		t.Fatal("SSIS factory plan blocked by its own metadata", err, result.Blockers)
	}
	order := map[string]int{}
	for i, step := range result.Steps {
		order[string(step.AssetID)] = i
	}
	if order[f.ids[dataFactoryType]] <= order[id] || order[id] <= order[f.ids[dataFactoryLinkedType]] || order[f.ids[dataFactoryLinkedType]] <= order[f.ids[dataFactoryDatasetType]] || order[f.ids[dataFactoryDatasetType]] <= order[f.ids[dataFactoryPipelineType]] {
		t.Fatal("SSIS prerequisites out of native order", order)
	}
}

func TestDataFactorySharedRuntimeRequiresExplicitConsumerSelection(t *testing.T) {
	f := newDataFactoryFixture(t)
	consumerRoot, consumer := dataFactorySharingFixture(t, f, "Key")
	values := f.assets(t)
	lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"host-only", "consumer-runtime", "consumer-factory", "consumer-retained"} {
		input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(f.ids[dataFactoryType])}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
		switch mode {
		case "consumer-runtime":
			input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, asset.AssetID(consumer))
		case "consumer-factory", "consumer-retained":
			input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, asset.AssetID(consumerRoot))
			if mode == "consumer-retained" {
				input.RequestOptions = map[asset.AssetID]map[string]any{asset.AssetID(consumerRoot): {"retain_resources": []string{consumer}}}
			}
		}
		result, err := plan.Solve(input)
		blocked := mode == "host-only" || mode == "consumer-retained"
		if err != nil || (len(result.Blockers) != 0) != blocked {
			t.Fatal("shared consumer selection changed", mode, err, result.Blockers)
		}
		if mode == "host-only" && slices.ContainsFunc(result.Steps, func(step plan.CleanupTaskStep) bool {
			return step.AssetID == asset.AssetID(consumer) || step.AssetID == asset.AssetID(consumerRoot)
		}) {
			t.Fatal("host deletion implicitly selected independent consumer")
		}
	}
}

func TestDataFactoryGraphRejectsChangedInventoryAndNativeContext(t *testing.T) {
	for _, mode := range []string{"proof", "connection", "duplicate", "creation", "private-child", "lock", "group-protection", "new-work", "missing-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			values := f.assets(t)
			root, child := f.ids[dataFactoryType], f.ids[dataFactoryDatasetType]
			switch mode {
			case "proof":
				values[0].Normalized[dataFactoryProof] = "forged"
			case "connection":
				values[0].Identity.ConnectionID = "other"
			case "duplicate":
				values = append(values, values[0])
			case "creation":
				object(f.resources[root]["properties"])["createTime"] = "2026-09-01T00:00:00Z"
			case "private-child":
				f.resources[child]["futurePrivateSetting"] = "changed"
			case "lock":
				f.locks = []any{map[string]any{"id": child + "/providers/Microsoft.Authorization/locks/new", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group-protection":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "new-work":
				run, _ := dataFactoryWorkFixture(t, f)
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					switch strings.ToLower(req.URL.Path) {
					case root + "/querypipelineruns":
						return jsonResponse(200, map[string]any{"value": []any{run}}, nil), true
					case root + "/pipelineruns/" + text(run["runId"]):
						return jsonResponse(200, run, nil), true
					}
					return previous(req)
				}
			case "missing-child":
				values = slices.DeleteFunc(values, func(value asset.Asset) bool { return value.Identity.NativeID == child })
			}
			lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
			contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
			if mode == "missing-child" {
				if err != nil || !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
					return ref.NativeID == child && ref.Relationship == graph.RelationshipAttachedTo
				}) {
					t.Fatal("missing reviewed child did not leave unresolved ownership", err)
				}
			} else if err == nil {
				t.Fatal("changed native context produced an authoritative graph", mode)
			}
		})
	}
}
