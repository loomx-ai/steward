package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNetappUpdateReceiptsSeparateDeletionAndVolumeIdentity(t *testing.T) {
	f := newNetappFixture(t)
	id := strings.ToLower(resourceID(netappAccountType, "first")) + "/capacitypools/item/volumes/item"
	uid := text(object(f.objects[id]["properties"])["fileSystemId"])
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
	h.Set("Location", netappTestPollURL("result_url"))
	update, err := c.netappUpdateReceipt(id, "eastus", uid, response{status: 202, header: h})
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := c.netappDeleteReceipt(id, "eastus", response{status: 202, header: h})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.netappPollUpdate(t.Context(), id, "eastus", uid, deletion); err == nil {
		t.Fatal("deletion receipt reused for update")
	}
	if _, err := c.netappPoll(t.Context(), id, "eastus", update); err == nil {
		t.Fatal("update receipt reused for deletion")
	}
	if _, err := c.netappPollUpdate(t.Context(), id, "eastus", testTenant, update); err == nil {
		t.Fatal("volume incarnation not bound")
	}
	for _, status := range []int{201, 204, 409} {
		if _, err := c.netappUpdateReceipt(id, "eastus", uid, response{status: status, header: h}); err == nil {
			t.Fatal("invalid update acceptance", status)
		}
	}
	for _, fault := range []string{"missing body", "wrong uuid", "wrong owner", "no callback", "foreign callback", "duplicate callback"} {
		t.Run(fault, func(t *testing.T) {
			res := response{status: 200, data: batchClone(f.objects[id]), header: http.Header{}}
			switch fault {
			case "missing body":
				res.data = nil
			case "wrong uuid":
				object(res.data["properties"])["fileSystemId"] = testTenant
			case "wrong owner":
				res.data["id"] = id + "2"
			case "no callback":
				res = response{status: 202, header: http.Header{}}
			case "foreign callback":
				res.header.Set("Location", strings.Replace(netappTestPollURL("result_url"), testSubscription, testTenant, 1))
			case "duplicate callback":
				res.header.Add("Location", netappTestPollURL("result_url"))
				res.header.Add("Location", netappTestPollURL("result_url"))
			}
			if _, err := c.netappUpdateReceipt(id, "eastus", uid, res); err == nil {
				t.Fatal("invalid update response accepted", fault)
			}
		})
	}
}
func TestNetappUpdatePollPhasesAndWrongTargets(t *testing.T) {
	for _, fault := range []string{"none", "wrong action", "wrong status", "wrong id", "wrong result uuid", "failure", "changed callback", "expired"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappFixture(t)
			id := strings.ToLower(resourceID(netappAccountType, "first")) + "/capacitypools/item/volumes/item"
			uid := text(object(f.objects[id]["properties"])["fileSystemId"])
			calls := 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				if !strings.Contains(strings.ToLower(q.URL.Path), "/operationresults/") {
					return nil, false
				}
				calls++
				if fault == "expired" {
					return jsonResponse(404, nil, nil), true
				}
				if q.URL.Query().Get("operationResultResponseType") == "Location" {
					raw := batchClone(f.objects[id])
					if fault == "wrong result uuid" {
						object(raw["properties"])["fileSystemId"] = testTenant
					}
					return jsonResponse(200, raw, nil), true
				}
				raw := netappTestStatus(id, "Succeeded")
				object(raw["properties"])["action"] = "PATCH"
				h := http.Header{}
				switch fault {
				case "wrong action":
					object(raw["properties"])["action"] = "DELETE"
				case "wrong status":
					raw["status"] = "Deleting"
				case "wrong id":
					object(raw["properties"])["resourceName"] = id + "2"
				case "failure":
					raw["status"] = "Failed"
					raw["error"] = map[string]any{"code": "Failed"}
				case "changed callback":
					h.Set("Location", strings.Replace(netappTestPollURL("result_url"), testTenant, testApplication, 1))
				}
				return jsonResponse(200, raw, h), true
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			h := http.Header{}
			h.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
			h.Set("Location", netappTestPollURL("result_url"))
			receipt, err := c.netappUpdateReceipt(id, "eastus", uid, response{status: 202, header: h})
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.netappPollUpdate(t.Context(), id, "eastus", uid, receipt)
			if fault != "none" && fault != "wrong result uuid" {
				if err == nil {
					t.Fatal("invalid status accepted", fault)
				}
				return
			}
			if err != nil || out.Done || out.Data["status_done"] != true {
				t.Fatal("status phase not saved", out, err)
			}
			wire, _ := json.Marshal(out.Data)
			if json.Unmarshal(wire, &receipt) != nil {
				t.Fatal("receipt round trip")
			}
			out, err = c.netappPollUpdate(t.Context(), id, "eastus", uid, receipt)
			if fault == "wrong result uuid" {
				if err == nil {
					t.Fatal("wrong final incarnation accepted")
				}
				return
			}
			if err != nil || !out.Done || calls != 2 {
				t.Fatal("final volume phase", out, err, calls)
			}
			if _, err := c.netappPollUpdate(t.Context(), id, "eastus", uid, out.Data); err != nil || calls != 2 {
				t.Fatal("completed poll repeated", err, calls)
			}
		})
	}
}

func TestNetappSnapshotPolicyAsyncUpdatesAndEventualReadback(t *testing.T) {
	f := newNetappPolicyFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	previous := f.override
	current := ""
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.Method == "PATCH" {
			_, ok := previous(q)
			if !ok {
				t.Fatal("patch fixture")
			}
			current = strings.ToLower(q.URL.Path)
			h := http.Header{}
			h.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
			h.Set("Location", netappTestPollURL("result_url"))
			return &http.Response{StatusCode: 202, Header: h, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if strings.Contains(strings.ToLower(q.URL.Path), "/operationresults/") {
			if q.URL.Query().Get("operationResultResponseType") == "Location" {
				return jsonResponse(200, f.objects[current], nil), true
			}
			raw := netappTestStatus(current, "Succeeded")
			object(raw["properties"])["action"] = "PATCH"
			return jsonResponse(200, raw, nil), true
		}
		return previous(q)
	}
	wait := func() {
		t.Helper()
		req.ExecutionResult = &result
		out, err := driver.Wait(t.Context(), req, result)
		if err != nil || out.Done {
			t.Fatal("update wait", out, err)
		}
		result.Data = out.Data
	}
	for _, id := range f.volumes {
		wait()
		wait() // accept then status
		if object(result.Data["operation"])["status_done"] != true {
			t.Fatal("status checkpoint")
		}
		wait()
		wait() // callback complete; own GET still has the old assignment
		if f.patches[id] != 1 {
			t.Fatal("patch replayed while own read lagged")
		}
		f.finishPatch(id)
		wait()
	}
	wait()
	wait()
	object(f.objects[f.policy]["properties"])["provisioningState"] = "Deleting"
	wait() // deleting policy remains present
	f.missing[f.policy] = true
	req.ExecutionResult = &result
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || !out.Done || f.policyDeletes != 1 {
		t.Fatal("asynchronous workflow", out, err)
	}
}

func TestNetappSnapshotPolicyPatchContract(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.NetApp.Volumes_Update")
	if !ok {
		t.Fatal("native update operation missing")
	}
	body := map[string]any{"properties": map[string]any{"dataProtection": map[string]any{"snapshot": map[string]any{"snapshotPolicyId": ""}}}}
	bound, err := bindAzureREST(op, map[string]any{"subscriptionId": testSubscription, "resourceGroupName": "test", "accountName": "first", "poolName": "item", "volumeName": "item", "body": body})
	if err != nil || bound.Method != "PATCH" {
		t.Fatal("native PATCH binding", bound, err)
	}
	want, _ := json.Marshal(body)
	if string(bound.Body) != string(want) || !strings.HasSuffix(bound.URL, "?api-version="+netappVersion) {
		t.Fatal("native snapshot unassignment contract", bound)
	}
}
