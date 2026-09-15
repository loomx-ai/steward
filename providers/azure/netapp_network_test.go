package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func netappNetworkFixture(t *testing.T) (*netappPoolFixture, map[string]any) {
	f := newNetappPoolFixture(t)
	subnet := text(object(f.objects[f.volumes[0]]["properties"])["subnetId"])
	for _, id := range f.volumes {
		p := object(f.objects[id]["properties"])
		p["networkSiblingSetId"] = testTenant
		p["subnetId"] = subnet
		p["mountTargets"] = []any{map[string]any{"ipAddress": "10.0.0.4", "fileSystemId": p["fileSystemId"], "mountTargetId": testApplication}}
	}
	raw := map[string]any{"networkSiblingSetId": testTenant, "subnetId": subnet, "networkSiblingSetStateId": "-42", "provisioningState": "Succeeded", "networkFeatures": "Standard", "nicInfoList": []any{map[string]any{"ipAddress": "10.0.0.4", "volumeResourceIds": []any{f.volumes[0], f.volumes[1]}}}, "futureSecret": "netapp-network-private-canary"}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(q.URL.Path, "/locations/eastus/queryNetworkSiblingSet") {
			var body map[string]any
			if q.Method != "POST" || q.URL.Query().Get("api-version") != netappVersion || len(q.URL.Query()) != 1 || !strings.HasSuffix(q.URL.Path, "/locations/eastus/queryNetworkSiblingSet") || json.NewDecoder(q.Body).Decode(&body) != nil || len(body) != 2 || body["networkSiblingSetId"] != testTenant || body["subnetId"] != subnet {
				t.Fatal("incorrect network query", q.URL, body)
			}
			return jsonResponse(200, raw, nil), true
		}
		return previous(q)
	}
	return f, raw
}
func netappNetworkItem(t *testing.T, f *netappPoolFixture) contracts.InventoryItem {
	t.Helper()
	req := netappRequest(f.runtime, netappVolumeType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	page, err := f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.NativeID == f.volumes[0] {
			return item
		}
	}
	t.Fatal("missing network volume")
	return contracts.InventoryItem{}
}
func TestNetappNetworkRecordedQuery(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/network-sibling-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var recording struct {
		Ref          string
		SHA          string `json:"source_sha256"`
		Interactions []struct {
			Index             int
			Method, URL, Body string
			Request           string `json:"request_body"`
			Status            int
		}
	}
	if json.Unmarshal(wire, &recording) != nil || len(recording.Interactions) != 1 || recording.SHA != "f4e2c3e9f3e8f1f86affd1c556ec49c474df592ecff0f91a180d29ffeca73f00" || recording.Ref != "ea185727729efc032ad9d4eef9ec355ee74ebaae" {
		t.Fatal("recording provenance")
	}
	row := recording.Interactions[0]
	if row.Index != 62 {
		t.Fatal("recording index")
	}
	u, err := url.Parse(row.URL)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]any
	if json.Unmarshal([]byte(row.Request), &original) != nil {
		t.Fatal("original request")
	}
	f := newNetappFixture(t)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = "00000000-0000-0000-0000-000000000000"
	calls := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		calls++
		var body map[string]any
		if q.Method != row.Method || !strings.EqualFold(q.URL.Path, u.Path) || q.URL.RawQuery != u.RawQuery || json.NewDecoder(q.Body).Decode(&body) != nil || len(body) != 2 || body["networkSiblingSetId"] != original["networkSiblingSetId"] || body["subnetId"] != strings.ToLower(text(original["subnetId"])) {
			t.Fatal("recorded request mismatch")
		}
		return &http.Response{StatusCode: row.Status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(row.Body))}, true
	}
	result, err := c.netappNetworkSet(t.Context(), "eastus", strings.ToLower(text(original["subnetId"])), text(original["networkSiblingSetId"]))
	if err != nil || calls != 1 || result["state"] != "-2124302775_46084.7739580787" || len(object(result["nics"])) != 1 {
		t.Fatal("unchanged recorded response", result, err)
	}
}
func TestNetappNetworkQueryFaults(t *testing.T) {
	for _, fault := range []string{"wrong set", "wrong subnet", "missing state", "numeric state", "unknown state", "unknown features", "missing nics", "duplicate ip", "duplicate volume", "invalid ip", "ipv6 zone", "wrong kind", "foreign volume", "empty nic", "next link", "error body", "async", "location", "accepted", "no content", "forbidden", "not found"} {
		t.Run(fault, func(t *testing.T) {
			f, raw := netappNetworkFixture(t)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			body := batchClone(raw)
			nic := object(array(body["nicInfoList"])[0])
			status := 200
			header := http.Header{}
			switch fault {
			case "wrong set":
				body["networkSiblingSetId"] = testApplication
			case "wrong subnet":
				body["subnetId"] = text(body["subnetId"]) + "-other"
			case "missing state":
				delete(body, "networkSiblingSetStateId")
			case "numeric state":
				body["networkSiblingSetStateId"] = 42
			case "unknown state":
				body["provisioningState"] = "Future"
			case "unknown features":
				body["networkFeatures"] = "Future"
			case "missing nics":
				delete(body, "nicInfoList")
			case "duplicate ip":
				body["nicInfoList"] = []any{nic, nic}
			case "duplicate volume":
				nic["volumeResourceIds"] = []any{f.volumes[0], f.volumes[0]}
			case "invalid ip":
				nic["ipAddress"] = "not-an-ip"
			case "ipv6 zone":
				nic["ipAddress"] = "fe80::1%en0"
			case "wrong kind":
				nic["volumeResourceIds"] = []any{f.id}
			case "foreign volume":
				nic["volumeResourceIds"] = []any{strings.Replace(f.volumes[0], testSubscription, testTenant, 1)}
			case "empty nic":
				nic["volumeResourceIds"] = []any{}
			case "next link":
				body["nextLink"] = "https://management.azure.com/next"
			case "error body":
				body["error"] = map[string]any{"code": "Failure"}
			case "async":
				header.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
			case "location":
				header.Set("Location", netappTestPollURL("result_url"))
			case "accepted":
				status = 202
			case "no content":
				status = 204
			case "forbidden":
				status = 403
			case "not found":
				status = 404
			}
			f.override = func(q *http.Request) (*http.Response, bool) { return jsonResponse(status, body, header), true }
			if _, err := c.netappNetworkSet(t.Context(), "eastus", text(raw["subnetId"]), testTenant); err == nil {
				t.Fatal("unreliable network response accepted", fault)
			}
		})
	}
}
func TestNetappNetworkInventoryAndMutationBoundary(t *testing.T) {
	for _, scenario := range []string{"stable", "updating", "transition", "missing peer", "wrong mount", "wrong peer set", "changed peer", "changing query", "added peer", "changed set", "omitted peer", "deleted peer", "owner omitted"} {
		t.Run(scenario, func(t *testing.T) {
			f, raw := netappNetworkFixture(t)
			item := netappNetworkItem(t, f)
			network := object(object(item.Normalized[netappVolumeReview])["network"])
			if len(object(network["members"])) != 2 || item.Actionable == nil || !*item.Actionable {
				t.Fatal("shared network not reviewed")
			}
			wire, _ := json.Marshal(item)
			if strings.Contains(string(wire), "netapp-network-private-canary") {
				t.Fatal("private query fields exposed")
			}
			value := asset.Asset{ID: "volume", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: netappVolumeType, NativeID: item.NativeID}, Normalized: item.Normalized, Location: item.Location}
			if scenario == "stable" {
				old := value
				old.Normalized = batchClone(value.Normalized)
				delete(object(old.Normalized[netappVolumeReview]), "network")
				if _, err := f.runtime.ResolveAction(t.Context(), "connection", old); err == nil {
					t.Fatal("old networkless review accepted")
				}
				return
			}
			switch scenario {
			case "updating":
				raw["provisioningState"] = "Updating"
			case "transition":
				raw["networkFeatures"] = "Basic_Standard"
			case "missing peer":
				f.readFault[f.volumes[1]] = 403
			case "wrong mount":
				object(array(object(f.objects[f.volumes[1]]["properties"])["mountTargets"])[0])["ipAddress"] = "10.0.0.5"
			case "wrong peer set":
				object(f.objects[f.volumes[1]]["properties"])["networkSiblingSetId"] = testApplication
			case "added peer":
				id := f.id + "/volumes/new"
				own := batchClone(f.objects[f.volumes[1]])
				own["id"], own["name"] = id, "new"
				props := object(own["properties"])
				props["fileSystemId"] = testTenant
				object(array(props["mountTargets"])[0])["fileSystemId"] = testTenant
				f.objects[id] = own
				object(array(raw["nicInfoList"])[0])["volumeResourceIds"] = []any{f.volumes[0], f.volumes[1], id}
			case "changed set":
				raw["networkSiblingSetStateId"] = "new-state"
			case "omitted peer", "deleted peer":
				object(array(raw["nicInfoList"])[0])["volumeResourceIds"] = []any{f.volumes[0]}
				if scenario == "deleted peer" {
					f.missing[f.volumes[1]] = true
					raw["networkSiblingSetStateId"] = "after-delete"
				}
			case "owner omitted":
				object(array(raw["nicInfoList"])[0])["volumeResourceIds"] = []any{f.volumes[1]}
			}
			previous := f.override
			reads, queries := 0, 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(q.URL.Path, "/queryNetworkSiblingSet") {
					queries++
					if scenario == "changing query" && queries == 2 {
						raw["networkSiblingSetStateId"] = "concurrent"
					}
				}
				if strings.EqualFold(q.URL.Path, f.volumes[1]) {
					reads++
					if scenario == "changed peer" && reads == 2 {
						object(f.objects[f.volumes[1]]["properties"])["fileSystemId"] = testTenant
					}
				}
				return previous(q)
			}
			if scenario == "updating" || scenario == "transition" {
				current := netappNetworkItem(t, f)
				if current.Actionable == nil || *current.Actionable {
					t.Fatal("unstable networking permits cleanup")
				}
				return
			}
			if scenario == "changed set" || scenario == "added peer" || scenario == "omitted peer" || scenario == "deleted peer" {
				req := contracts.ActionRequest{Asset: value, Action: "delete"}
				for id, entry := range object(object(value.Normalized[netappVolumeReview])["members"]) {
					member := object(entry)
					child := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: text(member["kind"]), NativeID: id}, Location: value.Location, Normalized: map[string]any{"_netapp_configuration": member["configuration"]}}
					req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: child, ControllerID: value.ID, Delete: true})
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				pre, err := driver.Preflight(t.Context(), req)
				if scenario == "deleted peer" {
					if err != nil || !pre.Allowed {
						t.Fatal("verified prior volume deletion blocked", pre, err)
					}
				} else if err == nil {
					t.Fatal("changed network authorized deletion", scenario)
				}
				if len(f.deletes) != 0 {
					t.Fatal("preflight mutated")
				}
				return
			}
			req := netappRequest(f.runtime, netappVolumeType)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			if _, err := f.runtime.List(t.Context(), req); err == nil {
				t.Fatal("inconsistent shared network accepted", scenario)
			}
		})
	}
}

func TestNetappNetworkWorkerPreservesFailedRescan(t *testing.T) {
	f, _ := netappNetworkFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	before := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	if len(before) != 26 {
		t.Fatal("shared fixture graph", len(before))
	}
	f.readFault[f.volumes[1]] = 503
	after := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, []string{netappVolumeType}, true, true)
	if len(after) != len(before) {
		t.Fatal("network read failure reconciled assets")
	}
}

func TestNetappNetworkQueryScope(t *testing.T) {
	for _, fault := range []string{"region path", "region case", "empty region", "subnet kind", "subnet case", "set uuid"} {
		t.Run(fault, func(t *testing.T) {
			f, raw := netappNetworkFixture(t)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			region, subnet, set := "eastus", text(raw["subnetId"]), testTenant
			switch fault {
			case "region path":
				region = "eastus/../westus"
			case "region case":
				region = "EastUS"
			case "empty region":
				region = ""
			case "subnet kind":
				subnet = f.id
			case "subnet case":
				subnet = strings.ToUpper(subnet)
			case "set uuid":
				set = "unknown"
			}
			calls := 0
			f.override = func(q *http.Request) (*http.Response, bool) { calls++; return jsonResponse(200, raw, nil), true }
			if _, err := c.netappNetworkSet(t.Context(), region, subnet, set); err == nil || calls != 0 {
				t.Fatal("invalid query scope sent", fault, calls, err)
			}
		})
	}
}

func TestNetappNetworkMalformedUUID(t *testing.T) {
	for _, value := range []any{nil, "", "network-private-invalid-uuid", 42, map[string]any{"private": "network-private-invalid-uuid"}} {
		f, _ := netappNetworkFixture(t)
		p := object(f.objects[f.volumes[1]]["properties"])
		p["fileSystemId"] = value
		delete(p, "mountTargets")
		req := netappRequest(f.runtime, netappVolumeType)
		req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
		page, err := f.runtime.List(t.Context(), req)
		if value == nil || value == "" {
			if err != nil || len(page.Items) != 2 {
				t.Fatal("missing UUID erased discovery", err)
			}
			for _, item := range page.Items {
				if item.Actionable == nil || *item.Actionable {
					t.Fatal("missing UUID permits cleanup")
				}
			}
		} else if err == nil || len(page.Items) != 0 {
			t.Fatal("malformed UUID accepted")
		}
		wire, _ := json.Marshal(page)
		if strings.Contains(string(wire), "network-private-invalid-uuid") {
			t.Fatal("malformed identifier exposed")
		}
	}
}
