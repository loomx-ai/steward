package gcp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestInfraManagerRejectsMalformedRetentionOptions(t *testing.T) {
	for _, options := range []map[string]any{
		{"retain_all_resources": "true"}, {"retain_all_resources": nil},
		{"retain_resources": true}, {"retain_resources": []any{true}},
		{"retain_resources": []string{""}}, {"retain_resources": []string{"unknown"}},
		{"retain_resources": []string{infraTestNetwork}}, // Its reviewed impact still says delete.
		{"deletePolicy": "ABANDON"}, {"delete_options": []any{}}, {"force": true},
	} {
		t.Run(strings.TrimSpace(text(options["retain_all_resources"]))+"option", func(t *testing.T) {
			s, driver, request := infraFixtureAction(t, false)
			request.Parameters = options
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.writes) != 0 {
				t.Fatalf("invalid options became a default destructive operation: %+v %v", options, err)
			}
		})
	}
}

func TestInfraManagerDoesNotSkipNativeResourcePreparation(t *testing.T) {
	for _, mode := range []string{"vm-ready", "vm-retain", "vm-protected", "group-ready", "group-retain-vm", "group-retain-stateful", "group-retain-disk", "group-protected"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedGroupFixture(t, false)
			f.live("vm")["deletionProtection"] = mode == "group-protected"
			var request contracts.ActionRequest
			driver := f.driver()
			if strings.HasPrefix(mode, "vm-") {
				vm := f.value("vm")
				vm.Normalized = groupCopy(f.live("vm"))
				request = contracts.ActionRequest{Asset: vm, Action: "delete", LifecycleImpacts: []contracts.ActionImpact{{Asset: f.value("boot"), ControllerID: vm.ID, Delete: mode != "vm-retain"}}}
				driver = protocolAction(t, instanceType, strings.TrimPrefix(vm.Identity.NativeID, "//compute.googleapis.com/"), f.roundTrip)
				if mode == "vm-protected" {
					f.live("vm")["deletionProtection"] = true
				}
			} else {
				retained := map[string]string{"group-retain-vm": "vm", "group-retain-stateful": "data", "group-retain-disk": "boot"}[mode]
				if retained == "" {
					request = f.request()
				} else {
					request = f.request(retained)
				}
			}
			driver.identity = request.Asset.Identity
			err := driver.infraNativeDeleteReady(context.Background(), request)
			if (err == nil) != strings.HasSuffix(mode, "-ready") || len(f.mutations) != 0 {
				t.Fatalf("native preparation readiness: %v mutations=%v", err, f.mutations)
			}
		})
	}
	t.Run("gke-finalizers", func(t *testing.T) {
		f := newGKENetworkFixture(t)
		request, _, _ := f.request()
		driver := f.driver()
		driver.identity = request.Asset.Identity
		if check, err := driver.Preflight(context.Background(), request); err != nil || !check.Allowed {
			t.Fatalf("GKE fixture must allow its normal preparation flow: %+v %v", check, err)
		}
		if err := driver.infraNativeDeleteReady(context.Background(), request); err == nil || len(f.mutations) != 0 {
			t.Fatal("deployment teardown may not bypass pending Kubernetes finalizers")
		}
	})
	t.Run("tpu-data-disks", func(t *testing.T) {
		s := newTPUScenario(t)
		r, values, result := tpuReviewed(t, s, tpuTestQueue)
		node := batchAsset(values, tpuTestNode)
		request := dataformRequest(t, result, values, node)
		raw, err := r.ResolveAction(context.Background(), "connection", node)
		if err != nil {
			t.Fatal(err)
		}
		driver := raw.(*action)
		if err := driver.infraNativeDeleteReady(context.Background(), request); err == nil {
			t.Fatal("deployment teardown may not bypass TPU data disk detachment")
		}
		delete(s.resources[tpuTestNode], "dataDisks")
		if err := driver.infraNativeDeleteReady(context.Background(), request); err != nil {
			t.Fatalf("detached ready TPU rejected: %v", err)
		}
		for _, call := range s.calls {
			if strings.HasPrefix(call, http.MethodDelete+" ") || strings.HasPrefix(call, http.MethodPatch+" ") {
				t.Fatal("readiness check changed a TPU")
			}
		}
	})
}
