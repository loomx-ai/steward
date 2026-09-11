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
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (f *communicationFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	var values []asset.Asset
	for _, kind := range communicationTestKinds {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil {
			t.Fatal("Communication asset inventory", kind, err)
		}
		for _, item := range batch.Items {
			value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: item.NativeID}, Location: item.Location, Name: item.Name, Normalized: item.Normalized}
			if item.Actionable != nil && *item.Actionable {
				value.Capabilities = asset.CapabilitySet{asset.CapabilityActionable}
			}
			values = append(values, value)
		}
	}
	data, _ := json.Marshal(values)
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	return values
}

func TestCommunicationNativeLifecycleAndPlans(t *testing.T) {
	f := newCommunicationFixture(t)
	values := f.assets(t)
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil || len(contribution.Unresolved) != 0 || len(contribution.Bindings) != 8 {
		t.Fatal("Communication native graph", err, contribution.Unresolved, contribution.Bindings)
	}
	comm, email, domain, phone := cdnAsset(t, values, communicationType), cdnAsset(t, values, communicationEmailType), cdnAsset(t, values, communicationDomainType), cdnAsset(t, values, communicationPhoneType)
	for _, binding := range contribution.Bindings {
		if binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || !binding.DirectCleanupAllowed {
			t.Fatal("native lifecycle authority changed", binding)
		}
		if binding.ManagedAssetID == phone.ID {
			if binding.ControllerAssetID != comm.ID || binding.CleanupPolicy != graph.CleanupDelegate || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
				t.Fatal("phone release lost account ownership or absence verification", binding)
			}
		} else if binding.CleanupPolicy != graph.CleanupDirect || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != nil {
			t.Fatal("native child bypassed its independent deletion", binding)
		}
	}
	for _, test := range []struct {
		name           string
		selected       []asset.AssetID
		steps, impacts int
		blocked        bool
	}{
		{"account", []asset.AssetID{comm.ID}, 4, 1, false},
		{"phone", []asset.AssetID{phone.ID}, 1, 0, false},
		{"email-shared-link", []asset.AssetID{email.ID}, 5, 0, true},
		{"domain-shared-link", []asset.AssetID{domain.ID}, 4, 0, true},
		{"explicit-account-and-email", []asset.AssetID{email.ID, comm.ID}, 9, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: test.selected, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
			if err != nil || (len(solved.Blockers) != 0) != test.blocked || len(solved.Steps) != test.steps || len(solved.ImpactItems) != test.impacts {
				t.Fatal("Communication plan changed native cleanup semantics", solved.Blockers, len(solved.Steps), len(solved.ImpactItems), err)
			}
			if test.name == "explicit-account-and-email" {
				request := servicePlanRequest(solved, values, domain)
				if !slices.ContainsFunc(request.PrerequisiteDeletions, func(impact contracts.ActionImpact) bool { return impact.Asset.ID == comm.ID && impact.Delete }) {
					t.Fatal("domain plan lost its explicitly selected linked account", request.PrerequisiteDeletions)
				}
			}
		})
	}
}

func TestCommunicationLifecycleBoundaries(t *testing.T) {
	for _, mode := range []string{"unscanned-phone", "domain-only", "root-only", "duplicate-native", "duplicate-asset", "changed-connection", "changed-partition", "forged-proof", "changed-private-config", "changed-roster", "omitted-phone", "known-phone-404", "native-403", "new-child", "changed-shared-link", "incoming-list-omission", "new-lock"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommunicationFixture(t)
			values := f.assets(t)
			phone, comm, domain := f.ids[communicationPhoneType], f.ids[communicationType], f.ids[communicationDomainType]
			switch mode {
			case "unscanned-phone":
				values = slices.DeleteFunc(values, func(value asset.Asset) bool { return value.Identity.NativeID == phone })
			case "domain-only", "root-only":
				kind := communicationDomainType
				if mode == "root-only" {
					kind = communicationEmailType
				}
				values = []asset.Asset{cdnAsset(t, values, kind)}
			case "duplicate-native":
				duplicate := values[0]
				duplicate.ID = "another-asset"
				values = append(values, duplicate)
			case "duplicate-asset":
				values[1].ID = values[0].ID
			case "changed-connection":
				values[0].Identity.ConnectionID = "another"
			case "changed-partition":
				values[0].Identity.Partition = "azure_china"
			case "forged-proof":
				values[0].Normalized[communicationProof] = "forged"
			case "changed-private-config":
				object(f.resources[comm]["properties"])["futurePrivateSetting"] = "changed"
			case "changed-roster":
				f.participants[f.ids[communicationRoomType]][0].(map[string]any)["rawId"] = "8:acs:changed"
			case "omitted-phone":
				f.omitted[phone] = true
			case "known-phone-404":
				delete(f.resources, phone)
			case "native-403":
				original := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, domain) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
					}
					return original(req)
				}
			case "new-child":
				id := comm + "/smtpusernames/another"
				raw := batchClone(f.resources[f.ids[communicationSMTPType]])
				raw["id"], raw["name"] = id, "another"
				f.resources[id], f.kinds[id] = raw, communicationSMTPType
			case "changed-shared-link":
				object(f.resources[comm]["properties"])["linkedDomains"] = []any{}
			case "incoming-list-omission":
				values = []asset.Asset{cdnAsset(t, values, communicationDomainType)}
				f.omitted[comm] = true
			case "new-lock":
				f.locks = []any{map[string]any{"id": comm + "/providers/microsoft.authorization/locks/retain", "properties": map[string]any{"level": "CanNotDelete"}}}
			}
			lifecycle, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
			contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
			valid := slices.Contains([]string{"unscanned-phone", "domain-only", "root-only", "omitted-phone", "incoming-list-omission", "new-lock"}, mode)
			if !valid {
				if err == nil {
					t.Fatal("changed native Communication graph accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal("valid native graph rejected", err)
			}
			if mode == "unscanned-phone" && !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
				return ref.NativeID == phone && ref.ControllerID == asset.AssetID(comm)
			}) {
				t.Fatal("unscanned native phone omitted from review")
			}
			if mode == "domain-only" || mode == "incoming-list-omission" {
				if !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
					return ref.NativeID == comm && ref.ControllerID == asset.AssetID(domain) && ref.Relationship == graph.RelationshipDependsOn && ref.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false
				}) {
					t.Fatal("domain-only selection bypassed an unselected linked account", contribution.Unresolved)
				}
			}
			if mode == "new-lock" {
				for _, binding := range contribution.Bindings {
					if binding.ControllerAssetID == asset.AssetID(comm) && binding.DirectCleanupAllowed {
						t.Fatal("locked account child acquired direct cleanup", binding)
					}
				}
			}
		})
	}
}
