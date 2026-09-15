package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestNetappIndependentChildNativeStates(t *testing.T) {
	for _, kind := range []string{netappSubvolumeType, netappQuotaType} {
		for _, mode := range []string{"succeeded", "failed", "updating", "unknown", "missing state", "missing target", "creation", "invalid creation", "invalid system data", "protected"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newNetappRecoveryFixture(t, kind)
				raw := f.objects[f.id]
				p := object(raw["properties"])
				p["provisioningState"] = "Succeeded"
				allowed := mode == "succeeded" || mode == "failed" || mode == "creation" || mode == "missing state" && kind == netappSubvolumeType
				switch mode {
				case "failed":
					p["provisioningState"] = "Failed"
				case "updating":
					p["provisioningState"] = "Updating"
				case "unknown":
					p["provisioningState"] = "Unknown"
				case "missing state":
					delete(p, "provisioningState")
				case "missing target":
					delete(p, "path")
					delete(p, "quotaTarget")
				case "creation":
					raw["systemData"] = map[string]any{"createdAt": "2026-03-03T17:00:41.656Z"}
				case "invalid creation":
					raw["systemData"] = map[string]any{"createdAt": "not-a-date"}
				case "invalid system data":
					raw["systemData"] = []any{}
				case "protected":
					raw["tags"] = map[string]any{"steward:protected": "true"}
				}
				value := f.asset(t)
				if value.Normalized["cleanup_protected"] != !allowed {
					t.Fatal("native eligibility", value.Normalized["cleanup_protected"], allowed)
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if !allowed {
					if err == nil {
						t.Fatal("ineligible child got direct action")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if pre, err := driver.Preflight(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil || !pre.Allowed {
					t.Fatal(pre, err)
				}
			})
		}
	}
}

func TestNetappQuotaNativeTypesAndChangedTarget(t *testing.T) {
	for _, quotaType := range []string{"DefaultUserQuota", "DefaultGroupQuota", "IndividualUserQuota", "IndividualGroupQuota"} {
		t.Run(quotaType, func(t *testing.T) {
			f := newNetappRecoveryFixture(t, netappQuotaType)
			p := object(f.objects[f.id]["properties"])
			p["quotaType"] = quotaType
			if strings.HasPrefix(quotaType, "Default") {
				p["quotaTarget"] = ""
			} else {
				p["quotaTarget"] = "1821"
			}
			value := f.asset(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			req := contracts.ActionRequest{Asset: value, Action: "delete"}
			if pre, err := driver.Preflight(t.Context(), req); err != nil || !pre.Allowed {
				t.Fatal(pre, err)
			}
			p["quotaTarget"] = "1822"
			if _, err := driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("changed quota target deleted", err)
			}
		})
	}
}

func TestNetappChildParentIncarnationAndConfiguration(t *testing.T) {
	for _, kind := range []string{netappSubvolumeType, netappQuotaType} {
		for _, change := range []string{"volume incarnation", "pool incarnation", "child creation", "child target"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				f := newNetappRecoveryFixture(t, kind)
				f.objects[f.id]["systemData"] = map[string]any{"createdAt": "2026-03-03T17:00:41.656Z"}
				value := f.asset(t)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				req := contracts.ActionRequest{Asset: value, Action: "delete"}
				result, err := driver.Execute(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				req.ExecutionResult = &result
				switch change {
				case "volume incarnation":
					object(f.objects[f.source]["properties"])["fileSystemId"] = testApplication
					f.missing[f.id] = true
				case "pool incarnation":
					object(f.objects[redisParentID(f.source)]["properties"])["poolId"] = testApplication
					f.missing[f.id] = true
				case "child creation":
					object(f.objects[f.id]["systemData"])["createdAt"] = "2026-03-04T17:00:41.656Z"
				case "child target":
					if kind == netappSubvolumeType {
						object(f.objects[f.id]["properties"])["path"] = "/different"
					} else {
						object(f.objects[f.id]["properties"])["quotaTarget"] = "1822"
					}
				}
				if _, err := driver.Readback(t.Context(), req); err == nil {
					t.Fatal("changed incarnation was accepted")
				}
			})
		}
	}
}

func TestNetappChildReplicationAndProtectionBlockFullGraphPlan(t *testing.T) {
	for _, kind := range []string{netappSubvolumeType, netappQuotaType} {
		for _, mode := range []string{"replication", "protected"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newNetappRecoveryFixture(t, kind)
				if mode == "protected" {
					f.objects[f.id]["tags"] = map[string]any{"steward:protected": "true"}
				} else {
					old := f.override
					f.override = func(q *http.Request) (*http.Response, bool) {
						if strings.HasSuffix(strings.ToLower(q.URL.Path), "/listreplications") && strings.HasPrefix(strings.ToLower(q.URL.Path), f.source) {
							return jsonResponse(200, map[string]any{"value": []any{map[string]any{"remoteVolumeResourceId": strings.Replace(f.source, "/first/", "/second/", 1), "remoteVolumeRegion": "westus"}}}, nil), true
						}
						return old(q)
					}
				}
				repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
				kinds := []string{}
				for _, definition := range netappResources {
					kinds = append(kinds, definition.kind)
				}
				values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
				var selected asset.Asset
				for _, v := range values {
					if v.Identity.NativeID == f.id {
						selected = v
					}
				}
				if selected.ID == "" {
					t.Fatal("missing child")
				}
				task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: selected.ID}}})
				if err != nil || len(task.Task.Blockers) == 0 {
					t.Fatal("unsafe child plan", task, err)
				}
				if f.deletes != 0 {
					t.Fatal("planning deleted child")
				}
			})
		}
	}
}

func TestNetappSubvolumeRequiresEnabledParent(t *testing.T) {
	f := newNetappRecoveryFixture(t, netappSubvolumeType)
	value := f.asset(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	object(f.objects[f.source]["properties"])["enableSubvolumes"] = "Disabled"
	if _, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"}); err == nil || f.deletes != 0 {
		t.Fatal("disabled parent ignored", err)
	}
	req := netappRequest(f.runtime, netappSubvolumeType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	req.KnownNativeIDs = []string{f.id}
	observed, err := f.runtime.List(t.Context(), req)
	if err != nil || len(observed.Items) != 1 || *observed.Items[0].Actionable {
		t.Fatal("known disabled child was lost or became actionable", err)
	}
}

func TestNetappQuotaReadbackKeepsLiveUpdatingRule(t *testing.T) {
	f := newNetappRecoveryFixture(t, netappQuotaType)
	value := f.asset(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	req := contracts.ActionRequest{Asset: value, Action: "delete"}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExecutionResult = &result
	object(f.objects[f.id]["properties"])["provisioningState"] = "Deleting"
	// Native lifecycle state can change after acceptance without fabricating a
	// different quota identity. A successful operation cannot erase a live rule.
	object(f.objects[f.id]["properties"])["quotaSizeInKiBs"] = json.Number("100006")
	read, err := driver.Readback(t.Context(), req)
	if err != nil || !read.Exists {
		t.Fatal(read, err)
	}
}
