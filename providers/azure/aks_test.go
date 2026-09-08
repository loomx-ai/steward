package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type aksScenario struct {
	cluster, group, vm, disk, unknown map[string]any
	members                           []any
	locks                             []any
	status                            int
	deletes, polls, groupReads        int
	clusterGone, groupGone            bool
	childrenGone                      bool
	operationFailed                   bool
}

func newAKSScenario() *aksScenario {
	group := "/subscriptions/" + testSubscription + "/resourcegroups/custom-nodes"
	s := &aksScenario{
		cluster: nativeResource(aksType, "cluster", "eastus", map[string]any{"nodeResourceGroup": "custom-nodes", "provisioningState": "Succeeded"}),
		group:   map[string]any{"id": group, "type": groupType, "location": "eastus", "name": "custom-nodes"},
		vm:      nativeResource(vmType, "node", "eastus", map[string]any{"provisioningState": "Succeeded", "storageProfile": map[string]any{"osDisk": map[string]any{"managedDisk": map[string]any{"id": group + "/providers/Microsoft.Compute/disks/boot"}, "deleteOption": "Delete"}}}),
		disk:    nativeResource(diskType, "boot", "eastus", map[string]any{"provisioningState": "Succeeded"}),
		unknown: nativeResource("Microsoft.Example/widgets", "custom", "eastus", map[string]any{}),
		locks:   []any{},
	}
	s.group["managedBy"] = s.cluster["id"]
	for _, raw := range []map[string]any{s.vm, s.disk, s.unknown} {
		raw["id"] = strings.Replace(text(raw["id"]), "/resourceGroups/test/", "/resourceGroups/custom-nodes/", 1)
	}
	s.members = []any{s.vm, s.disk, s.unknown}
	return s
}

func (s *aksScenario) runtime(t *testing.T) *Runtime {
	t.Helper()
	return protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		group := strings.ToLower(text(s.group["id"]))
		operation := "/subscriptions/" + testSubscription + "/providers/microsoft.containerservice/locations/eastus/operations/delete-cluster"
		switch path {
		case strings.ToLower(text(s.cluster["id"])):
			if req.Method == "DELETE" {
				s.deletes++
				if req.URL.Query().Get("api-version") != "2024-02-01" || req.Header.Get("x-ms-client-request-id") != azureRequestID("delete-aks") {
					t.Errorf("invalid native AKS deletion %s %+v", req.URL, req.Header)
				}
				return jsonResponse(202, map[string]any{}, http.Header{"Azure-Asyncoperation": {apiURL(operation, "2024-02-01")}, "X-Ms-Request-Id": {"aks-delete"}}), nil
			}
			if s.clusterGone {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
			}
			return jsonResponse(200, s.cluster, http.Header{"X-Ms-Request-Id": {"aks-get"}}), nil
		case "/subscriptions/" + testSubscription + "/resourcegroups/test":
			return jsonResponse(200, map[string]any{"id": path}, nil), nil
		case group:
			s.groupReads++
			if s.groupGone {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceGroupNotFound"}}, nil), nil
			}
			return jsonResponse(200, s.group, nil), nil
		case group + "/resources":
			if s.status != 0 {
				return jsonResponse(s.status, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), nil
			}
			if req.URL.Query().Get("$skiptoken") == "second" {
				return jsonResponse(200, map[string]any{"value": s.members[1:]}, nil), nil
			}
			if len(s.members) < 2 {
				return jsonResponse(200, map[string]any{"value": s.members}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": s.members[:1], "nextLink": apiURL(group+"/resources", resourcesVersion) + "&%24skiptoken=second"}, nil), nil
		case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": s.locks}, nil), nil
		case strings.ToLower(text(s.vm["id"])):
			if s.childrenGone {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, s.vm, nil), nil
		case strings.ToLower(text(s.disk["id"])):
			if s.childrenGone {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, s.disk, nil), nil
		case operation:
			s.polls++
			state := "Succeeded"
			if s.operationFailed {
				state = "Failed"
			}
			return jsonResponse(200, map[string]any{"status": state}, nil), nil
		}
		return nil, fmt.Errorf("unexpected AKS operation %s %s", req.Method, req.URL)
	})
}

func (s *aksScenario) assets(t *testing.T) []asset.Asset {
	t.Helper()
	var result []asset.Asset
	for _, raw := range []map[string]any{s.cluster, s.group, s.vm, s.disk, s.unknown} {
		payload, _ := json.Marshal(object(raw["properties"]))
		var normalized map[string]any
		if err := json.Unmarshal(payload, &normalized); err != nil {
			t.Fatal(err)
		}
		if normalized == nil {
			normalized = map[string]any{}
		}
		normalized["subscription_id"] = testSubscription
		id, nativeType, err := parseID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		if kind, found := findType(nativeType); found {
			nativeType = kind.NativeType
		}
		result = append(result, asset.Asset{ID: asset.AssetID(last(id)), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure-public", NativeType: nativeType, NativeID: id, ScopeKey: "eastus"}, Location: "eastus", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: normalized})
	}
	result[1].Identity.ScopeKey = "global"
	return result
}

func aksRequest(t *testing.T, r *Runtime, assets []asset.Asset) (contracts.ActionRequest, plan.Result) {
	t.Helper()
	contributor, err := r.ClusterLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatalf("contribution=%+v err=%v", contribution, err)
	}
	attachments, err := NewResourceAttachments().Contribute(context.Background(), "scope", assets)
	if err != nil || len(attachments.Bindings) != 0 {
		t.Fatalf("attachment conflicts with AKS ownership: %+v %v", attachments, err)
	}
	result, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{"cluster"}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || len(result.ImpactItems) != len(assets)-1 {
		t.Fatalf("plan steps=%d impacts=%d blockers=%+v err=%v", len(result.Steps), len(result.ImpactItems), result.Blockers, err)
	}
	for _, step := range result.Steps {
		if step.AssetID != "cluster" && step.Kind != plan.StepVerification {
			t.Fatalf("AKS child has a direct delete step: %+v", step)
		}
	}
	request := contracts.ActionRequest{Asset: assets[0], Action: "delete", IdempotencyKey: "delete-aks"}
	for _, item := range result.ImpactItems {
		for _, value := range assets {
			if value.ID == item.AssetID {
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: item.ControllerID, Delete: item.Expected == plan.ExpectedDelegatedDelete})
			}
		}
	}
	return request, result
}

func TestAKSPlansNativeGroupAndUnknownResourcesAndWaitsForGroupAbsence(t *testing.T) {
	s := newAKSScenario()
	r := s.runtime(t)
	request, _ := aksRequest(t, r, s.assets(t))
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "aks-delete" || result.ProviderOperationID == "" || s.deletes != 1 {
		t.Fatalf("execute=%+v err=%v deletes=%d", result, err, s.deletes)
	}
	// Persist operation state and create a fresh driver as the worker does after
	// a restart. Operation success and cluster absence do not prove group absence.
	payload, _ := json.Marshal(result)
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	driver, err = s.runtime(t).ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	s.clusterGone = true
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "deleting_node_resource_group" {
		t.Fatalf("wait prematurely succeeded: %+v %v", wait, err)
	}
	s.groupGone = true
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "deleting_aks_resources" {
		t.Fatalf("surviving children overlooked: %+v %v", wait, err)
	}
	s.childrenGone = true
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || s.deletes != 1 {
		t.Fatalf("wait=%+v err=%v deletes=%d", wait, err, s.deletes)
	}
}

func TestAKSRejectsChangedImpactScopeProtectionAndPermission(t *testing.T) {
	tests := []struct {
		name   string
		change func(*aksScenario, *contracts.ActionRequest)
	}{
		{"new resource", func(s *aksScenario, _ *contracts.ActionRequest) {
			s.members = append(s.members, map[string]any{"id": text(s.group["id"]) + "/providers/Microsoft.Example/widgets/new", "type": "Microsoft.Example/widgets"})
		}},
		{"missing impact", func(_ *aksScenario, r *contracts.ActionRequest) { r.LifecycleImpacts = nil }},
		{"retained impact", func(_ *aksScenario, r *contracts.ActionRequest) { r.LifecycleImpacts[0].Delete = false }},
		{"foreign connection", func(_ *aksScenario, r *contracts.ActionRequest) {
			r.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
		}},
		{"foreign partition", func(_ *aksScenario, r *contracts.ActionRequest) {
			r.LifecycleImpacts[0].Asset.Identity.Partition = "foreign"
		}},
		{"wrong controller", func(_ *aksScenario, r *contracts.ActionRequest) { r.LifecycleImpacts[0].ControllerID = "foreign" }},
		{"duplicate impact", func(_ *aksScenario, r *contracts.ActionRequest) {
			r.LifecycleImpacts = append(r.LifecycleImpacts, r.LifecycleImpacts[0])
		}},
		{"changed group", func(s *aksScenario, _ *contracts.ActionRequest) {
			object(s.cluster["properties"])["nodeResourceGroup"] = "other"
		}},
		{"foreign owner", func(s *aksScenario, _ *contracts.ActionRequest) { s.group["managedBy"] = resourceID(aksType, "other") }},
		{"malformed member", func(s *aksScenario, _ *contracts.ActionRequest) {
			s.members = append(s.members, map[string]any{"id": text(s.group["id"]) + "/providers/Microsoft.Compute/disks/wrong", "type": vmType})
		}},
		{"duplicate member", func(s *aksScenario, _ *contracts.ActionRequest) { s.members = append(s.members, s.vm) }},
		{"foreign member", func(s *aksScenario, _ *contracts.ActionRequest) {
			s.members = append(s.members, nativeResource(diskType, "outside", "eastus", nil))
		}},
		{"resource lock", func(s *aksScenario, _ *contracts.ActionRequest) {
			s.locks = []any{map[string]any{"id": text(s.disk["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
		}},
		{"protected tag", func(s *aksScenario, _ *contracts.ActionRequest) {
			s.unknown["tags"] = map[string]any{"steward/protected": "true"}
		}},
		{"permission", func(s *aksScenario, _ *contracts.ActionRequest) { s.status = 403 }},
		{"external disk", func(s *aksScenario, _ *contracts.ActionRequest) {
			object(object(object(s.vm["properties"])["storageProfile"])["osDisk"])["managedDisk"] = map[string]any{"id": resourceID(diskType, "external")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := newAKSScenario()
			r := s.runtime(t)
			request, _ := aksRequest(t, r, s.assets(t))
			test.change(s, &request)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || s.deletes != 0 {
				t.Fatalf("unreviewed cluster deletion accepted: err=%v deletes=%d", err, s.deletes)
			}
		})
	}
}

func TestAKSGraphUnresolvedAndRetentionBlockedBeforeExecution(t *testing.T) {
	s := newAKSScenario()
	r := s.runtime(t)
	assets := s.assets(t)
	contributor, err := r.ClusterLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", assets[:len(assets)-1])
	if err != nil || len(contribution.Unresolved) != 1 || !strings.EqualFold(contribution.Unresolved[0].NativeID, text(s.unknown["id"])) {
		t.Fatalf("missing member not unresolved: %+v %v", contribution, err)
	}
	contribution, err = contributor.Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{"boot", assets[3].Identity.NativeID} {
		result, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{"cluster"}, LifecycleBindings: contribution.Bindings, RequestOptions: map[asset.AssetID]map[string]any{"cluster": {"retain_resources": []string{retained}}}})
		if err != nil || len(result.Blockers) != 1 || result.Blockers[0].Code != plan.BlockLifecycleAuthority {
			t.Fatalf("unsupported retention not blocked: %+v %v", result, err)
		}
	}
	for _, binding := range contribution.Bindings {
		if binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true || binding.DirectCleanupAllowed {
			t.Fatalf("incorrect AKS ownership: %+v", binding)
		}
	}
}

func TestAKSOperationFailureDoesNotDeclareDeleted(t *testing.T) {
	s := newAKSScenario()
	r := s.runtime(t)
	request, _ := aksRequest(t, r, s.assets(t))
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.operationFailed = true
	s.clusterGone = true
	s.groupGone = true
	if wait, err := driver.Wait(context.Background(), request, result); err == nil || wait.Done {
		t.Fatalf("failed operation succeeded: %+v %v", wait, err)
	}
}

func TestAKSAlreadyAbsentClusterDoesNotHideSurvivingNodeGroup(t *testing.T) {
	s := newAKSScenario()
	r := s.runtime(t)
	request, _ := aksRequest(t, r, s.assets(t))
	s.clusterGone = true
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed || check.Absent {
		t.Fatalf("node group incorrectly considered gone: %+v %v", check, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || s.deletes != 0 {
		t.Fatalf("absent cluster delete resubmitted: deletes=%d err=%v", s.deletes, err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("live node group prematurely completed: %+v %v", wait, err)
	}
	s.groupGone = true
	check, err = driver.Preflight(context.Background(), request)
	if err != nil || check.Absent {
		t.Fatalf("surviving children overlooked: %+v %v", check, err)
	}
	s.childrenGone = true
	check, err = driver.Preflight(context.Background(), request)
	if err != nil || !check.Absent {
		t.Fatalf("absence not confirmed: %+v %v", check, err)
	}
}

func TestAKSDependentCollectionNotFoundIsNotClusterAbsence(t *testing.T) {
	s := newAKSScenario()
	r := s.runtime(t)
	request, _ := aksRequest(t, r, s.assets(t))
	s.status = 404
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(context.Background(), request)
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation || check.Absent {
		t.Fatalf("missing collection mistaken for cluster absence: %+v %v", check, err)
	}
}
