package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Compose a retained native Monitor resource with the existing native AKS
// managed-group protocol. Only its ARM identity moves; private properties and
// the selected product GET/LIST contracts remain intact. Group ownership is
// composed evidence, not an assertion made by the published Monitor example.
func monitorManagedScenario(t *testing.T, f *monitorInventoryFixture) (*aksScenario, *Runtime, []asset.Asset, string) {
	t.Helper()
	s := newAKSScenario()
	group := strings.ToLower(text(s.group["id"]))
	oldID := slices.Sorted(maps.Keys(f.objects))[0]
	raw := f.objects[oldID]
	id := group + "/providers/" + strings.ToLower(f.kind) + "/" + last(oldID)
	raw["id"], raw["name"] = id, last(id)
	clear(f.objects)
	f.objects[id] = raw
	clear(f.groups)
	f.groups[group] = s.group
	s.members = []any{raw}
	r := s.runtime(t)
	aksTransport := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/resourcegroups" || path != group && f.groups[path] != nil || strings.Contains(path, "/providers/microsoft.eventhub/") || strings.Contains(path, "/providers/microsoft.operationalinsights/") {
			return f.runtime.transport.RoundTrip(req)
		}
		if _, _, _, err := monitorResourceID(path); err == nil {
			return f.runtime.transport.RoundTrip(req)
		}
		for _, kind := range monitorInventoryKinds() {
			if strings.HasSuffix(path, "/providers/"+strings.ToLower(kind)) {
				return f.runtime.transport.RoundTrip(req)
			}
		}
		return aksTransport.RoundTrip(req)
	})
	values := s.assets(t)[:2]
	member := f.asset(t, id)
	member.Identity.Partition = values[0].Identity.Partition
	values = append(values, member)
	return s, r, values, id
}

func changeManagedMonitor(kind string, raw map[string]any) {
	props := object(raw["properties"])
	switch kind {
	case monitorActionGroupType:
		object(array(props["emailReceivers"])[0])["emailAddress"] = "PRIVATE_CHANGED_RECEIVER"
	case insightsWebTestType:
		object(props["Configuration"])["WebTest"] = "PRIVATE_CHANGED_WEBTEST"
	case monitorConsumptionBudgetType, monitorCostBudgetType:
		for _, notification := range object(props["notifications"]) {
			object(notification)["contactEmails"] = []any{"PRIVATE_CHANGED_RECEIVER"}
		}
	default:
		props["description"] = "changed after review"
	}
}

func TestMonitorManagedGroupConfiguration(t *testing.T) {
	for _, kind := range monitorInventoryKinds() {
		for _, mode := range []string{"delayed", "graph-change", "preflight-change", "readback-change", "group-change", "reference-proof", "native-202", "native-lro", "native-alias", "native-denied", "native-missing"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, kind)
				s, r, values, id := monitorManagedScenario(t, f)
				if mode == "graph-change" {
					changeManagedMonitor(kind, f.objects[id])
					contributor, err := r.ClusterLifecycle(t.Context(), "connection")
					if err != nil {
						t.Fatal(err)
					}
					if result, err := contributor.Contribute(t.Context(), "scope", values); err == nil || len(result.Bindings) != 0 {
						t.Fatal("changed Monitor member acquired group ownership", result, err)
					}
					return
				}
				request, _ := aksRequest(t, r, values)
				encoded, _ := json.Marshal(request)
				for _, secret := range []string{"emailAddress", "contactEmails", "WebTest=", "webHookProperties"} {
					if strings.Contains(string(encoded), secret) {
						t.Fatal("private managed Monitor content escaped projection", secret)
					}
				}
				driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "preflight-change":
					changeManagedMonitor(kind, f.objects[id])
				case "group-change":
					s.group["tags"] = map[string]any{"owner": "changed"}
				case "reference-proof":
					for i := range request.LifecycleImpacts {
						if request.LifecycleImpacts[i].Asset.Identity.NativeID == id {
							request.LifecycleImpacts[i].Asset.Normalized[monitorReferencesProof] = "forged"
						}
					}
				}
				if mode == "preflight-change" || mode == "group-change" || mode == "reference-proof" {
					if _, err := driver.Execute(t.Context(), request); err == nil || s.deletes != 0 || len(f.deletes) != 0 {
						t.Fatal("changed managed Monitor reached a mutation", err)
					}
					return
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil || s.deletes != 1 || len(f.deletes) != 0 {
					t.Fatal("native group deletion failed or mutated Monitor independently", result, err)
				}
				s.clusterGone, s.groupGone = true, true
				if json.Unmarshal(encoded, &request) != nil {
					t.Fatal("invalid recovered request")
				}
				driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
					t.Fatal("group absence hid residual Monitor resource", wait, err)
				}
				if mode == "readback-change" {
					changeManagedMonitor(kind, f.objects[id])
				}
				if strings.HasPrefix(mode, "native-") {
					reads := 0
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.ToLower(req.URL.Path) != id {
							return nil, false
						}
						reads++
						if reads == 1 {
							return nil, false // Generic residual GET saw a live root.
						}
						status, body, header := 200, maps.Clone(f.objects[id]), http.Header{}
						switch mode {
						case "native-202":
							status = 202
						case "native-lro":
							header.Set("Azure-AsyncOperation", apiURL(id, f.version))
						case "native-alias":
							body["Properties"] = body["properties"]
						case "native-denied":
							status = 403
						case "native-missing":
							status = 404
						}
						return jsonResponse(status, body, header), true
					}
				}
				if mode != "delayed" {
					if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
						t.Fatal("changed/incomplete residual Monitor read accepted", wait, err)
					}
					return
				}
				delete(f.objects, id)
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done || s.deletes != 1 || len(f.deletes) != 0 {
					t.Fatal("native residual absence failed after recovery", wait, err)
				}
			})
		}
	}
}

func TestMonitorManagedReceiverResolution(t *testing.T) {
	for _, phase := range []string{"graph", "preflight", "readback"} {
		t.Run(phase, func(t *testing.T) {
			f := newMonitorReceiverFixture(t)
			s, r, values, _ := monitorManagedScenario(t, f.monitorInventoryFixture)
			var request contracts.ActionRequest
			var result contracts.ActionResult
			if phase != "graph" {
				request, _ = aksRequest(t, r, values)
				if phase == "readback" {
					driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
					if err != nil {
						t.Fatal(err)
					}
					result, err = driver.Execute(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
					s.clusterGone, s.groupGone = true, true
				}
			}
			// Same Action Group and namespace name, different native resource
			// group. Its private configuration is unchanged; resolution drift
			// alone must prevent reuse of the original reviewed membership.
			namespace := f.receivers[f.namespaceID]
			delete(f.receivers, f.namespaceID)
			newID := strings.Replace(f.namespaceID, "/receiver-group/", "/replacement-group/", 1)
			namespace["id"] = newID
			f.receivers[newID] = namespace
			if phase == "graph" {
				contributor, err := r.ClusterLifecycle(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := contributor.Contribute(t.Context(), "scope", values); err == nil {
					t.Fatal("changed receiver resolution acquired group ownership")
				}
			} else {
				driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if phase == "preflight" {
					if _, err := driver.Execute(t.Context(), request); err == nil || s.deletes != 0 {
						t.Fatal("receiver drift reached group deletion", err)
					}
				} else if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
					t.Fatal("residual receiver resolution drift ignored", wait, err)
				}
			}
		})
	}
}

func TestMonitorApplicationInsightsManagedGroup(t *testing.T) {
	for _, kind := range []string{monitorActionGroupType, insightsWebTestType, monitorCostBudgetType} {
		for _, mode := range []string{"delayed", "private-change", "omitted"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newInsightsComponentFixture(t)
				clear(f.children)
				m := newMonitorInventoryFixture(t, kind)
				oldID := slices.Sorted(maps.Keys(m.objects))[0]
				raw := m.objects[oldID]
				if kind == insightsWebTestType {
					raw["tags"] = map[string]any{"hidden-link:" + f.parentID: "Resource"}
				}
				if kind == monitorCostBudgetType {
					m.groupOnly = true
					for _, notification := range object(object(raw["properties"])["notifications"]) {
						object(notification)["contactGroups"] = []any{}
					}
				}
				id := f.managedID + "/providers/" + strings.ToLower(m.kind) + "/" + last(oldID)
				raw["id"], raw["name"] = id, last(id)
				clear(m.objects)
				m.objects[id] = raw
				m.groups = f.groups
				f.members[id] = raw
				if mode == "omitted" {
					delete(f.members, id)
				}
				f.response = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if path == "/subscriptions/"+testSubscription+"/resources" {
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
					}
					_, _, _, err := monitorResourceID(path)
					monitor := err == nil
					for _, kind := range monitorInventoryKinds() {
						monitor = monitor || strings.HasSuffix(path, "/providers/"+strings.ToLower(kind))
					}
					if monitor {
						response, err := m.runtime.transport.RoundTrip(req)
						if err != nil {
							t.Fatal(err)
						}
						return response, true
					}
					return nil, false
				}
				request, planned, _ := insightsComponentPlan(t, f, m.kind)
				if len(planned.Steps) != 1 || len(request.LifecycleImpacts) != 3 || len(request.PrerequisiteDeletions) != 0 {
					t.Fatal("Monitor lost native managed-workspace delegation", planned, request)
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "private-change" {
					changeManagedMonitor(m.kind, raw)
					if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes)+len(m.deletes) != 0 {
						t.Fatal("changed Monitor reached component deletion", err)
					}
					return
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil || len(f.deletes) != 1 || len(m.deletes) != 0 {
					t.Fatal("component managed-group deletion failed", result, err)
				}
				encoded, _ := json.Marshal(request)
				if json.Unmarshal(encoded, &request) != nil {
					t.Fatal("invalid recovered component request")
				}
				driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
					t.Fatal("component/group absence hid live Monitor member", wait, err)
				}
				delete(m.objects, id)
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
					t.Fatal("managed Monitor residual absence failed", wait, err)
				}
			})
		}
	}
}
