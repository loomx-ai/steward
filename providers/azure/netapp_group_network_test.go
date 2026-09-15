package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func netappGroupNetworkFixture(t *testing.T) (*netappFixture, string, string, []map[string]any) {
	t.Helper()
	f, group, volume := netappGroupFixture(t)
	p := object(f.objects[volume]["properties"])
	p["mountTargets"] = []any{
		map[string]any{"ipAddress": "10.0.0.4", "fileSystemId": p["fileSystemId"], "mountTargetId": testTenant},
		map[string]any{"ipAddress": "10.0.0.5", "fileSystemId": p["fileSystemId"], "mountTargetId": testApplication},
	}
	nics := []map[string]any{}
	for i, ip := range []string{"10.0.0.4", "10.0.0.5"} {
		uid := testTenant
		if i == 1 {
			uid = testApplication
		}
		nic := nativeResource(nicType, "nic"+string(rune('a'+i)), "eastus", map[string]any{"provisioningState": "Succeeded", "resourceGuid": uid, "hostedWorkloads": []any{volume}, "futureSecret": "netapp-group-network-private-canary", "ipConfigurations": []any{map[string]any{"properties": map[string]any{"privateIPAddress": ip, "subnet": map[string]any{"id": p["subnetId"]}}}}})
		nics = append(nics, nic)
	}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if strings.HasSuffix(path, "/providers/microsoft.network/networkinterfaces") {
			if q.Method != "GET" || q.URL.Query().Get("api-version") != "2024-05-01" {
				t.Fatal("NIC index protocol", q.Method, q.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{nics[0], nics[1]}}, nil), true
		}
		for _, nic := range nics {
			if strings.EqualFold(text(nic["id"]), path) {
				if q.Method != "GET" || q.URL.Query().Get("api-version") != "2024-05-01" {
					t.Fatal("NIC own-read protocol", q.Method, q.URL)
				}
				return jsonResponse(200, nic, nil), true
			}
		}
		return previous(q)
	}
	return f, group, volume, nics
}
func TestNetappGroupNetworkPrimaryAndSecondaryInterfaces(t *testing.T) {
	f, group, volume, nics := netappGroupNetworkFixture(t)
	item := netappAssignmentItem(t, f, netappGroupType, nil)
	network := object(object(item.Normalized[netappGroupReview])["network"])
	if network["complete"] != true || len(object(network["interfaces"])) != 2 || len(object(network["addresses"])) != 2 {
		t.Fatal("incomplete primary/secondary review", network)
	}
	for _, nic := range nics {
		entry := object(object(network["interfaces"])[strings.ToLower(text(nic["id"]))])
		if entry["correlated"] != true || len(entry["workloads"].([]string)) != 1 || entry["workloads"].([]string)[0] != volume {
			t.Fatal("NIC correlation", entry)
		}
	}
	if item.Normalized["cleanup_protected"] != true || item.Actionable == nil || *item.Actionable {
		t.Fatal("correlation authorized cleanup")
	}
	wire, _ := json.Marshal(item)
	if strings.Contains(string(wire), "private-canary") {
		t.Fatal("private NIC data exposed")
	}
	// A stale index cannot replace own reads or hide known live interfaces.
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.network/networkinterfaces") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return previous(q)
	}
	again := netappAssignmentItem(t, f, netappGroupType, map[string]map[string]any{group: item.Normalized})
	if len(object(object(object(again.Normalized[netappGroupReview])["network"])["interfaces"])) != 2 {
		t.Fatal("live omitted interface lost")
	}
}
func TestNetappGroupNetworkIncompleteCorrelationStaysProtected(t *testing.T) {
	for _, fault := range []string{"missing mounts", "missing interfaces", "missing uuid", "missing workloads", "unknown workload", "shared workload", "wrong workload", "updating"} {
		t.Run(fault, func(t *testing.T) {
			f, _, volume, nics := netappGroupNetworkFixture(t)
			p := object(nics[0]["properties"])
			switch fault {
			case "missing mounts":
				delete(object(f.objects[volume]["properties"]), "mountTargets")
			case "missing interfaces":
				nics[0]["location"], nics[1]["location"] = "westus", "westus"
			case "missing uuid":
				delete(p, "resourceGuid")
			case "missing workloads":
				delete(p, "hostedWorkloads")
			case "unknown workload":
				p["hostedWorkloads"] = []any{"unqualified-private-canary"}
			case "shared workload":
				p["hostedWorkloads"] = []any{volume, redisParentID(volume) + "/volumes/other"}
			case "wrong workload":
				p["hostedWorkloads"] = []any{redisParentID(volume) + "/volumes/other"}
			case "updating":
				p["provisioningState"] = "Updating"
			}
			item := netappAssignmentItem(t, f, netappGroupType, nil)
			network := object(object(item.Normalized[netappGroupReview])["network"])
			if network["complete"] != false || item.Normalized["cleanup_protected"] != true {
				t.Fatal("incomplete NIC evidence accepted", network)
			}
			wire, _ := json.Marshal(item)
			if strings.Contains(string(wire), "private-canary") {
				t.Fatal("unknown workload leaked")
			}
		})
	}
}
func TestNetappGroupNetworkRejectsFaults(t *testing.T) {
	for _, fault := range []string{"index forbidden", "index absent", "duplicate index", "foreign index", "own forbidden", "own absent", "own identity", "own uuid", "duplicate uuid", "own async", "own error", "duplicate address", "invalid secondary mount", "wrong mount uuid", "invalid mount id", "invalid mount list", "invalid NIC IP", "invalid NIC list", "NIC changed", "NIC recreated", "late NIC", "known foreign", "known disappeared reappeared"} {
		t.Run(fault, func(t *testing.T) {
			f, group, volume, nics := netappGroupNetworkFixture(t)
			item := netappAssignmentItem(t, f, netappGroupType, nil)
			known := map[string]map[string]any{group: item.Normalized}
			nicID := strings.ToLower(text(nics[0]["id"]))
			p := object(nics[0]["properties"])
			mount := object(object(f.objects[volume]["properties"])["mountTargets"].([]any)[1])
			switch fault {
			case "duplicate uuid":
				object(nics[1]["properties"])["resourceGuid"] = p["resourceGuid"]
			case "own uuid":
				p["resourceGuid"] = map[string]any{"secret": "private-canary"}
			case "duplicate address":
				object(object(object(nics[1]["properties"])["ipConfigurations"].([]any)[0])["properties"])["privateIPAddress"] = "10.0.0.4"
			case "invalid secondary mount":
				mount["ipAddress"] = "invalid"
			case "wrong mount uuid":
				mount["fileSystemId"] = testSubscription
			case "invalid mount id":
				mount["mountTargetId"] = false
			case "invalid mount list":
				object(f.objects[volume]["properties"])["mountTargets"] = map[string]any{}
			case "invalid NIC IP":
				object(object(p["ipConfigurations"].([]any)[0])["properties"])["privateIPAddress"] = "bad"
			case "invalid NIC list":
				p["ipConfigurations"] = map[string]any{}
			case "known foreign":
				object(object(object(known[group][netappGroupReview])["network"])["interfaces"])[strings.Replace(nicID, testSubscription, testTenant, 1)] = map[string]any{}
			}
			previous := f.override
			reads, index := 0, 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if strings.HasSuffix(path, "/providers/microsoft.network/networkinterfaces") {
					index++
					switch fault {
					case "index forbidden":
						return jsonResponse(403, nil, nil), true
					case "index absent":
						return jsonResponse(404, nil, nil), true
					case "duplicate index":
						return jsonResponse(200, map[string]any{"value": []any{nics[0], nics[0]}}, nil), true
					case "foreign index":
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": strings.Replace(nicID, testSubscription, testTenant, 1)}}}, nil), true
					case "known disappeared reappeared":
						return jsonResponse(200, map[string]any{"value": []any{nics[1]}}, nil), true
					case "late NIC":
						if index == 2 {
							nics[1]["location"] = "westus"
						}
					}
				}
				if path == nicID {
					reads++
					switch fault {
					case "own forbidden":
						return jsonResponse(403, nil, nil), true
					case "own absent":
						return jsonResponse(404, nil, nil), true
					case "own async":
						return jsonResponse(202, nics[0], nil), true
					case "own error":
						raw := batchClone(nics[0])
						raw["error"] = map[string]any{"code": "Denied"}
						return jsonResponse(200, raw, nil), true
					case "own identity":
						raw := batchClone(nics[0])
						raw["id"] = nics[1]["id"]
						return jsonResponse(200, raw, nil), true
					case "NIC changed":
						if reads == 2 {
							p["enableIPForwarding"] = true
						}
					case "NIC recreated":
						if reads == 2 {
							p["resourceGuid"] = testSubscription
						}
					case "known disappeared reappeared":
						if reads == 1 {
							return jsonResponse(404, nil, nil), true
						}
					}
				}
				return previous(q)
			}
			req := netappRequest(f.runtime, netappGroupType)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			req.KnownNativeIDs = []string{group}
			req.KnownNativeMetadata = known
			page, err := f.runtime.List(t.Context(), req)
			if err == nil || page.Complete || len(page.AbsentNativeIDs) > 0 {
				t.Fatal("unsafe group network scan", fault, page, err)
			}
		})
	}
}

func TestNetappGroupNetworkPagedIndexUsesOwnReads(t *testing.T) {
	f, _, _, nics := netappGroupNetworkFixture(t)
	previous := f.override
	pages := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.network/networkinterfaces") {
			pages++
			// The list identifies candidates; its embedded IP and workload data aren't authority.
			if q.URL.Query().Get("$skiptoken") == "next" {
				return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": nics[1]["id"]}}}, nil), true
			}
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": nics[0]["id"], "properties": map[string]any{"hostedWorkloads": []any{}, "ipConfigurations": []any{}}}}, "nextLink": q.URL.String() + "&$skiptoken=next"}, nil), true
		}
		return previous(q)
	}
	item := netappAssignmentItem(t, f, netappGroupType, nil)
	if object(object(item.Normalized[netappGroupReview])["network"])["complete"] != true || pages != 8 {
		t.Fatal("paging/own-read correlation", pages, object(object(item.Normalized[netappGroupReview])["network"])["complete"])
	}
}
