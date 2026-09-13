package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (f *hybridCleanupFixture) standardMachine() map[string]any {
	raw := f.values[strings.ToLower(resourceID(hybridMachineType, "machine"))]
	delete(object(raw["properties"]), "parentClusterResourceId")
	return raw
}

func (f *hybridCleanupFixture) machineRequestAsset(t *testing.T) contracts.ActionRequest {
	t.Helper()
	f.standardMachine()
	request := f.requestAsset(t, hybridMachineType)
	for _, kind := range []string{hybridExtensionType, hybridCommandType, hybridProfileType} {
		child := f.requestAsset(t, kind).Asset
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: child, ControllerID: request.Asset.ID, Delete: true})
	}
	return request
}

func TestHybridComputeMachineEligibility(t *testing.T) {
	for _, kind := range []string{"", "AWS", "GCP", "HCI", "VMware", "SCVMM", "AVS", "EPS", "future-controller", "cluster"} {
		t.Run(kind, func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			raw := f.standardMachine()
			raw["kind"] = kind
			if kind == "cluster" {
				raw["kind"] = nil
				object(raw["properties"])["parentClusterResourceId"] = resourceID("Microsoft.AzureStackHCI/clusters", "hci")
			}
			batch, err := f.runtime.List(t.Context(), f.request(hybridMachineType))
			allowed := kind == "" || kind == "AWS" || kind == "GCP"
			if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable != allowed || batch.Items[0].Normalized["cleanup_protected"] != !allowed {
				t.Fatal("machine registration eligibility", err, kind)
			}
			if !allowed && batch.Items[0].Normalized["cleanup_protection_reason"] != "hybrid_compute_machine_controller_required" {
				t.Fatal("missing controller boundary")
			}
		})
	}
}

func TestHybridComputeMachineOrderedDeleteAndResiduals(t *testing.T) {
	f := newHybridCleanupFixture(t)
	request := f.machineRequestAsset(t)
	driver := f.driver(t, request)
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
		t.Fatal("live children allowed machine DELETE")
	}
	// Omitted native indexes still require each saved child's own GET.
	for _, child := range request.PrerequisiteDeletions {
		f.omitted[child.Asset.Identity.NativeID] = true
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
		t.Fatal("omission hid child")
	}
	residualID := request.PrerequisiteDeletions[0].Asset.Identity.NativeID
	residual := f.values[residualID]
	for _, child := range request.PrerequisiteDeletions {
		delete(f.values, child.Asset.Identity.NativeID)
	}
	f.values[request.Asset.Identity.NativeID]["etag"] = "changed-after-reviewed-children"
	f.hold = true
	result, err := driver.Execute(t.Context(), request)
	if err != nil || f.deleted[request.Asset.Identity.NativeID] != 1 {
		t.Fatal("machine DELETE", err)
	}
	for range 3 {
		wait, err := driver.Wait(t.Context(), request, result)
		if err != nil || wait.Done {
			t.Fatal("operation success closed live machine", err)
		}
		result.Data = wait.Data
		encoded, _ := json.Marshal(result)
		if json.Unmarshal(encoded, &result) != nil {
			t.Fatal("restore receipt")
		}
	}
	delete(f.values, request.Asset.Identity.NativeID)
	f.values[residualID] = residual
	request.ExecutionResult = &result
	if read, err := driver.Readback(t.Context(), request); err != nil || !read.Exists {
		t.Fatal("parent 404 hid reviewed child", err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
		t.Fatal("parent 404 completed residual cleanup", err)
	}
	if _, err := driver.Execute(t.Context(), request); err != nil || f.deleted[request.Asset.Identity.NativeID] != 1 {
		t.Fatal("resume repeated DELETE", err)
	}
	delete(f.values, residualID)
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("own subtree absence did not complete", err)
	}
	request.PrerequisiteDeletions = request.PrerequisiteDeletions[1:]
	if _, err := driver.Wait(t.Context(), request, result); err == nil {
		t.Fatal("restored request lost reviewed prerequisite")
	}
	if f.values[strings.ToLower(resourceID(hybridLicenseType, "license"))] == nil {
		t.Fatal("shared license deleted")
	}
}

func TestHybridComputeMachineMutationBoundaries(t *testing.T) {
	for _, mode := range []string{"configuration", "registration", "controller", "root tag", "group tag", "lock", "machine permission", "child permission", "index permission", "late child", "wrong parent", "retained child", "duplicate child", "foreign connection", "foreign partition", "foreign location", "forged child", "forged root", "missing root", "already deleting"} {
		t.Run(mode, func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			request := f.machineRequestAsset(t)
			driver := f.driver(t, request)
			root := f.values[request.Asset.Identity.NativeID]
			childID := request.PrerequisiteDeletions[0].Asset.Identity.NativeID
			child := f.values[childID]
			for _, prerequisite := range request.PrerequisiteDeletions {
				delete(f.values, prerequisite.Asset.Identity.NativeID)
			}
			failPath := ""
			switch mode {
			case "configuration":
				object(root["properties"])["futurePrivateConfiguration"] = "changed"
			case "registration":
				object(root["properties"])["vmId"] = testTenant
			case "controller":
				root["kind"] = "HCI"
			case "root tag":
				root["tags"] = map[string]any{"steward/protected": "true"}
			case "group tag":
				f.group["tags"] = map[string]any{"steward/protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": text(f.group["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "machine permission":
				failPath = request.Asset.Identity.NativeID
			case "child permission":
				failPath = childID
				delete(f.values, request.Asset.Identity.NativeID)
			case "index permission":
				failPath = request.Asset.Identity.NativeID + "/runcommands"
			case "late child":
				child["id"], child["name"] = childID+"-new", last(childID)+"-new"
				f.values[childID+"-new"] = child
			case "wrong parent":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "retained child":
				request.PrerequisiteDeletions[0].Delete = false
			case "duplicate child":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "foreign connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "foreign partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "other"
			case "foreign location":
				request.PrerequisiteDeletions[0].Asset.Location = "westus2"
			case "forged child":
				request.PrerequisiteDeletions[0].Asset.Normalized[hybridComputeCleanupProof] = "forged"
			case "forged root":
				object(request.Asset.Normalized[hybridComputeCleanup])["members"] = map[string]any{}
			case "missing root":
				delete(f.values, request.Asset.Identity.NativeID)
			case "already deleting":
				object(root["properties"])["provisioningState"] = "Deleting"
			}
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, failPath) {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				}
				return previous(req)
			}
			_, err := driver.Execute(t.Context(), request)
			verificationOnly := mode == "missing root" || mode == "already deleting"
			if (err == nil) != verificationOnly || len(f.deleted) != 0 {
				t.Fatal("unsafe machine mutation", err, mode)
			}
		})
	}
}

func TestHybridComputeEmptyMachineETagAndSynchronousDelete(t *testing.T) {
	for _, status := range []int{0, 204, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			f.standardMachine()
			for id, raw := range f.values {
				if hybridComputeChild(text(raw["type"])) {
					delete(f.values, id)
				}
			}
			request := f.requestAsset(t, hybridMachineType)
			driver := f.driver(t, request)
			if status == 0 {
				f.values[request.Asset.Identity.NativeID]["etag"] = "changed"
				if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deleted) != 0 {
					t.Fatal("unexplained ETag change accepted")
				}
				return
			}
			f.deleteStatus, f.hold = status, true
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
				t.Fatal("synchronous response proved absence", err)
			}
			delete(f.values, request.Asset.Identity.NativeID)
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
				t.Fatal("own 404", err)
			}
		})
	}
}

func TestHybridComputeMachineGraphAndKnownOmissions(t *testing.T) {
	f := newHybridCleanupFixture(t)
	request := f.machineRequestAsset(t)
	rootID := request.Asset.Identity.NativeID
	root := f.values[rootID]
	known := f.request(hybridMachineType)
	known.KnownNativeIDs = []string{rootID}
	known.KnownNativeMetadata = map[string]map[string]any{rootID: request.Asset.Normalized}
	for _, child := range request.PrerequisiteDeletions {
		f.omitted[child.Asset.Identity.NativeID] = true
	}
	batch, err := f.runtime.List(t.Context(), known)
	if err != nil || len(batch.Items) != 1 || len(object(object(batch.Items[0].Normalized[hybridComputeCleanup])["members"])) != 3 {
		t.Fatal("saved child omissions lost", err)
	}
	cascades := &serviceCascades{client: f.client, connectionID: "connection"}
	contribution := governance.Contribution{}
	if err := cascades.contributeHybridComputeMachines(t.Context(), []asset.Asset{request.Asset}, &contribution); err != nil || len(contribution.Unresolved) != 3 {
		t.Fatal("unscanned children not unresolved", err, contribution)
	}
	values := []asset.Asset{request.Asset}
	for _, prerequisite := range request.PrerequisiteDeletions {
		values = append(values, prerequisite.Asset)
	}
	contribution = governance.Contribution{}
	if err := cascades.contributeHybridComputeMachines(t.Context(), values, &contribution); err != nil || len(contribution.Bindings) != 3 || len(contribution.Unresolved) != 0 {
		t.Fatal("known omitted child graph", err)
	}
	for _, binding := range contribution.Bindings {
		if binding.CleanupPolicy != graph.CleanupDirect || !binding.DirectCleanupAllowed || binding.Ownership != graph.OwnershipExclusive {
			t.Fatal("child was treated as implicit cascade")
		}
	}
	// A partial root scan after child deletion retains explicit child absence steps.
	delete(f.values, request.PrerequisiteDeletions[0].Asset.Identity.NativeID)
	contribution = governance.Contribution{}
	if err := cascades.contributeHybridComputeMachines(t.Context(), values, &contribution); err != nil || len(contribution.Bindings) != 3 {
		t.Fatal("own-absent child blocked reconciliation", err)
	}
	delete(f.values, rootID)
	contribution = governance.Contribution{}
	if err := cascades.contributeHybridComputeMachines(t.Context(), values, &contribution); err != nil || len(contribution.Bindings) != 3 {
		t.Fatal("parent absence erased reviewed children", err)
	}
	live := f.values[request.PrerequisiteDeletions[1].Asset.Identity.NativeID]
	object(live["properties"])["futurePrivateConfiguration"] = "changed"
	contribution = governance.Contribution{}
	if err := cascades.contributeHybridComputeMachines(t.Context(), values, &contribution); err == nil {
		t.Fatal("changed child accepted")
	}
	// Forged saved hints fail before any new inventory is accepted.
	f.values[rootID] = root
	known.KnownNativeMetadata[rootID][hybridComputeCleanupProof] = "forged"
	if _, err := f.runtime.List(t.Context(), known); err == nil {
		t.Fatal("forged machine history accepted")
	}
}

func TestHybridComputeMachinePlanRetentionAndProtection(t *testing.T) {
	for _, mode := range []string{"ordinary", "retain", "protected"} {
		t.Run(mode, func(t *testing.T) {
			f := newHybridCleanupFixture(t)
			request := f.machineRequestAsset(t)
			values := []asset.Asset{request.Asset}
			for _, prerequisite := range request.PrerequisiteDeletions {
				values = append(values, prerequisite.Asset)
			}
			if mode == "protected" {
				raw := f.values[values[1].Identity.NativeID]
				raw["tags"] = map[string]any{"steward/protected": "true"}
				batch, err := f.runtime.List(t.Context(), f.request(values[1].Identity.NativeType))
				if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable {
					t.Fatal("protected child inventory", err)
				}
				values[1].Normalized = batch.Items[0].Normalized
				values[1].Capabilities = nil
			}
			contribution := governance.Contribution{}
			cascades := &serviceCascades{client: f.client, connectionID: "connection"}
			if err := cascades.contributeHybridComputeMachines(t.Context(), values, &contribution); err != nil {
				t.Fatal(err)
			}
			input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{request.Asset.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
			if mode == "retain" {
				input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_resources": []string{values[1].Identity.NativeID}}}
			}
			solved, err := plan.Solve(input)
			if err != nil || (len(solved.Blockers) == 0) != (mode == "ordinary") {
				t.Fatal("machine plan bypassed child boundary", mode, err, solved.Blockers)
			}
			if mode == "ordinary" {
				frozen := servicePlanRequest(solved, values, request.Asset)
				if len(solved.Steps) != 4 || len(frozen.PrerequisiteDeletions) != 3 || len(frozen.LifecycleImpacts) != 0 {
					t.Fatal("machine plan did not preserve independent deletion steps")
				}
			}
		})
	}
}
