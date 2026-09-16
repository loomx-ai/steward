package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestDeploymentStackPreparationStage(t *testing.T) {
	for _, mode := range []string{"complete", "pending", "failed", "forbidden", "job", "state", "nested", "unknown", "root_missing", "dns_changed", "completed_changed", "noop_changed"} {
		t.Run(mode, func(t *testing.T) {
			c := directClient(nil)
			req, root := stackAttachmentRequest(t, c)
			live := attachmentResources()
			if mode == "noop_changed" {
				storage := object(object(live["vm"]["properties"])["storageProfile"])
				object(storage["osDisk"])["deleteOption"] = "Detach"
				object(storage["dataDisks"].([]any)[0])["deleteOption"] = "Detach"
				ip := object(object(live["nic"]["properties"])["ipConfigurations"].([]any)[0])
				object(object(object(ip["properties"])["publicIPAddress"])["properties"])["deleteOption"] = "Detach"
			}
			for i := range req.LifecycleImpacts {
				if raw := live[string(req.LifecycleImpacts[i].Asset.ID)]; raw != nil && mode != "noop_changed" {
					req.LifecycleImpacts[i].Asset.Normalized["_arm_generation"] = productGeneration(raw)
				}
			}
			order, err := c.deploymentStackPreparationOrder(req)
			if err != nil || !slices.Equal(order, []asset.AssetID{"vm"}) {
				t.Fatal("VM-covered NIC prepared twice", order, err)
			}
			calls, writes, polls := 0, []string{}, map[string]int{}
			fault := false
			callbacks := map[string]string{}
			c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method == "DELETE" {
					t.Fatal("preparation stage deleted resource")
				}
				if name, found := callbacks[q.URL.String()]; found {
					polls[name]++
					status := "Succeeded"
					if polls[name] == 1 {
						status = "Running"
					}
					if mode == "failed" {
						status = "Failed"
					}
					return jsonResponse(200, map[string]any{"status": status}, nil), nil
				}
				if strings.EqualFold(q.URL.Path, req.Asset.Identity.NativeID) {
					if fault && mode == "root_missing" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, root, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(q.URL.Path), "/locks") || strings.EqualFold(q.URL.Path, text(live["vm"]["id"])+"/extensions") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(q.URL.Path), "/resourcegroups/test") {
					return jsonResponse(200, map[string]any{"id": q.URL.Path}, nil), nil
				}
				for name, raw := range live {
					if !strings.EqualFold(q.URL.Path, text(raw["id"])) {
						continue
					}
					if q.Method == "GET" {
						return jsonResponse(200, raw, nil), nil
					}
					if name != "nic" && name != "vm" || q.Method != "PUT" && q.Method != "PATCH" {
						t.Fatal("unexpected preparation write", name, q.Method)
					}
					if mode == "forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					var body map[string]any
					if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if q.Method == "PUT" {
						previous := object(raw["properties"])
						raw["properties"] = body["properties"]
						for _, key := range []string{"resourceGuid", "virtualMachine", "privateEndpoint", "hostedWorkloads"} {
							if value, found := previous[key]; found {
								object(raw["properties"])[key] = value
							}
						}
					} else {
						for key, value := range object(body["properties"]) {
							object(raw["properties"])[key] = value
						}
					}
					raw["etag"] = "after-" + name
					writes = append(writes, q.Method+" "+name)
					headers := http.Header{}
					if mode == "pending" || mode == "failed" {
						namespace, version := "Microsoft.Network", "2024-05-01"
						if name == "vm" {
							namespace = "Microsoft.Compute"
							kind, _ := findType(vmType)
							version = kind.Version
						}
						callback := apiURL("/subscriptions/"+testSubscription+"/providers/"+namespace+"/locations/eastus/operations/retain-"+name, version)
						callbacks[callback] = name
						headers.Set("Azure-AsyncOperation", callback)
					}
					return jsonResponse(200, raw, headers), nil
				}
				t.Fatalf("unexpected preparation stage request %s %s", q.Method, q.URL)
				return nil, nil
			})
			before, _ := json.Marshal(req)
			out, err := c.deploymentStackAdvancePreparations(t.Context(), req, nil)
			if mode == "forbidden" {
				if err == nil || out.Data != nil || out.Done {
					t.Fatal("forbidden write accepted", out, err)
				}
				return
			}
			if err != nil || out.Done {
				t.Fatal("first preparation failed", out, err)
			}
			fault = true
			for step := 0; step < 12; step++ {
				wire, err := json.Marshal(out.Data)
				var saved map[string]any
				if err != nil || json.Unmarshal(wire, &saved) != nil {
					t.Fatal("persist stage", err)
				}
				if step == 0 {
					switch mode {
					case "job":
						req.IdempotencyKey = "other-job"
					case "state":
						saved["binding"] = "changed"
					case "nested":
						object(object(saved["state"])["active"])["binding"] = "changed"
					case "unknown":
						object(saved["state"])["ignore_changes"] = true
					case "dns_changed":
						object(object(live["nic"]["properties"])["dnsSettings"])["dnsServers"] = []any{"10.0.0.9"}
					}
				}
				priorCalls, priorWrites := calls, len(writes)
				out, err = c.deploymentStackAdvancePreparations(t.Context(), req, saved)
				if mode != "complete" && mode != "pending" && mode != "completed_changed" && mode != "noop_changed" {
					if err == nil || out.Done || out.Data != nil || len(writes) != 1 {
						t.Fatal("invalid preparation resumed", out, err, writes)
					}
					if (mode == "job" || mode == "state" || mode == "nested" || mode == "unknown") && calls != priorCalls {
						t.Fatal("invalid preparation evidence reached HTTP")
					}
					return
				}
				if err != nil || len(writes) > priorWrites+1 {
					t.Fatal("stage failed or chained writes", out, err, writes)
				}
				if out.Done {
					break
				}
			}
			if !out.Done {
				t.Fatal("preparation never completed", out)
			}
			state := out.Data["state"].(deploymentStackPreparationState)
			if len(state.Completed) != 1 || state.Completed[0]["member"] != "vm" || len(object(state.Completed[0]["configurations"])) != 2 {
				t.Fatal("missing complete VM/NIC evidence", state)
			}
			if mode == "noop_changed" {
				if len(writes) != 0 {
					t.Fatal("already retained resources rewritten", writes)
				}
			} else if !slices.Equal(writes, []string{"PUT nic", "PATCH vm"}) {
				t.Fatal("retention writes repeated or unordered", writes)
			}
			if mode == "pending" && (polls["nic"] != 2 || polls["vm"] != 2) {
				t.Fatal("native preparation wait skipped", polls)
			}
			wire, _ := json.Marshal(out.Data)
			var saved map[string]any
			if err = json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			if mode == "completed_changed" || mode == "noop_changed" {
				object(object(live["vm"]["properties"])["hardwareProfile"])["vmSize"] = "Standard_D8s_v3"
			}
			priorWrites, priorCalls := len(writes), calls
			out, err = c.deploymentStackAdvancePreparations(t.Context(), req, saved)
			if mode == "completed_changed" || mode == "noop_changed" {
				if err == nil || out.Done || out.Data != nil {
					t.Fatal("completed checkpoint hid changed configuration", out, err)
				}
			} else if err != nil || !out.Done || calls <= priorCalls {
				t.Fatal("completed preparation not rechecked", out, err)
			}
			if len(writes) != priorWrites {
				t.Fatal("completed resume rewrote retention")
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("preparation changed frozen request")
			}
		})
	}
}

func TestDeploymentStackPreparationSelection(t *testing.T) {
	c := directClient(nil)
	req, root := stackAttachmentRequest(t, c)
	slices.Reverse(req.LifecycleImpacts)
	order, err := c.deploymentStackPreparationOrder(req)
	if err != nil || !slices.Equal(order, []asset.AssetID{"vm"}) {
		t.Fatal("reordering lost VM coverage", order, err)
	}
	// A flat native NIC with a reviewed VM Delete option shares the VM's
	// preparation context, including its retained public IP.
	var nic string
	for i := range req.LifecycleImpacts {
		if req.LifecycleImpacts[i].Asset.ID == "nic" {
			req.LifecycleImpacts[i].ControllerID = req.Asset.ID
			nic = req.LifecycleImpacts[i].Asset.Identity.NativeID
		}
	}
	properties := object(root["properties"])
	properties["resources"] = append(properties["resources"].([]any), map[string]any{"id": nic, "status": "managed", "denyStatus": "none"})
	review, err := c.deploymentStackMemberReview(root)
	if err != nil {
		t.Fatal(err)
	}
	req.Asset.Normalized[deploymentStackReviewKey] = review
	req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)
	order, err = c.deploymentStackPreparationOrder(req)
	if err != nil || !slices.Equal(order, []asset.AssetID{"vm"}) {
		t.Fatal("flat native NIC prepared separately from its VM cascade", order, err)
	}
}
