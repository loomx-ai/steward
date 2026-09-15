package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappBackupPolicyFixture struct {
	*netappPolicyFixture
	status  map[string]any
	pending map[string]string
}

func newNetappBackupPolicyFixture(t *testing.T) *netappBackupPolicyFixture {
	base := newNetappPoolFixture(t)
	f := &netappBackupPolicyFixture{netappPolicyFixture: &netappPolicyFixture{netappPoolFixture: base, policy: redisParentID(base.id) + "/backuppolicies/item", patches: map[string]int{}}, status: map[string]any{}, pending: map[string]string{}}
	p := object(f.objects[f.policy]["properties"])
	p["backupPolicyId"], p["provisioningState"] = testTenant, "Succeeded"
	p["volumesAssigned"], p["volumeBackups"] = 2, []any{}
	for _, id := range f.volumes {
		object(f.objects[id]["properties"])["dataProtection"] = map[string]any{
			"backup":   map[string]any{"backupPolicyId": f.policy, "policyEnforced": true, "backupVaultId": redisParentID(base.id) + "/backupvaults/item"},
			"snapshot": map[string]any{"snapshotPolicyId": redisParentID(base.id) + "/snapshotpolicies/item"},
		}
	}
	for _, raw := range f.objects {
		if raw["type"] == netappBackupType {
			object(raw["properties"])["backupPolicyResourceId"] = f.policy
		}
	}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		id := strings.ToLower(q.URL.Path)
		if q.Method == "GET" && strings.HasSuffix(id, "/latestbackupstatus/current") {
			volume := strings.TrimSuffix(id, "/latestbackupstatus/current")
			if !slices.Contains(f.volumes, volume) {
				t.Fatal("unreviewed status request")
			}
			state := f.status[volume]
			if state == nil {
				state = "Idle"
			}
			return jsonResponse(200, map[string]any{"relationshipStatus": state}, nil), true
		}
		if q.Method == "PATCH" {
			if !slices.Contains(f.volumes, id) || len(q.URL.Query()) != 1 || q.URL.Query().Get("api-version") != netappVersion {
				t.Fatal("unreviewed backup PATCH", q.URL)
			}
			var body map[string]any
			if json.NewDecoder(q.Body).Decode(&body) != nil {
				t.Fatal("invalid backup PATCH")
			}
			backup := object(object(object(body["properties"])["dataProtection"])["backup"])
			field := "policyEnforced"
			value := any(false)
			if _, exists := backup["backupPolicyId"]; exists {
				field, value = "backupPolicyId", ""
				if netappBackupEnforced(f.objects[id]) != false {
					t.Fatal("unassignment before suspension")
				}
			}
			want := map[string]any{"properties": map[string]any{"dataProtection": map[string]any{"backup": map[string]any{field: value}}}}
			gotJSON, _ := json.Marshal(body)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatal("unrelated fields in PATCH", string(gotJSON))
			}
			if f.status[id] != nil && f.status[id] != "Idle" {
				t.Fatal("mutation during transfer")
			}
			if f.pending[id] != "" {
				t.Fatal("replayed accepted update")
			}
			f.pending[id] = field
			f.patches[id]++
			if f.failAfterMutation {
				f.readFault[id] = 503
			}
			raw := batchClone(f.objects[id])
			object(raw["properties"])["provisioningState"] = "Updating"
			return jsonResponse(200, raw, nil), true
		}
		if q.Method == "DELETE" && id == f.policy {
			for _, volume := range f.volumes {
				assignments, _ := netappAssignments(f.objects[volume])
				if assignments[netappBackupPolicyType] != "" || netappBackupEnforced(f.objects[volume]) != false {
					t.Fatal("policy delete before unassignment")
				}
			}
			if len(q.URL.Query()) != 1 || q.ContentLength != 0 {
				t.Fatal("unreviewed DELETE")
			}
			f.policyDeletes++
			if f.failAfterMutation {
				f.readFault[id] = 503
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		return previous(q)
	}
	return f
}

func (f *netappBackupPolicyFixture) finishPatch(id string) {
	p := object(f.objects[id]["properties"])
	backup := object(object(p["dataProtection"])["backup"])
	if f.pending[id] == "policyEnforced" {
		backup["policyEnforced"] = false
	} else {
		backup["backupPolicyId"] = ""
	}
	delete(f.pending, id)
	p["provisioningState"] = "Succeeded"
	f.objects[id]["etag"] = "updated"
	count := 0
	for _, volume := range f.volumes {
		assignments, _ := netappAssignments(f.objects[volume])
		if assignments[netappBackupPolicyType] == f.policy {
			count++
		}
	}
	object(f.objects[f.policy]["properties"])["volumesAssigned"] = count
	f.objects[f.policy]["etag"] = "membership-changed"
	delete(f.readFault, id)
}

func TestNetappBackupPolicyPlanRetainsVolumes(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
	testNetappPolicyPlanPreservesVolumes(t, f.netappPolicyFixture, plan.WarningNetappBackupPolicyDelete)
}

func TestNetappBackupPolicyWorkerRestarts(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
	testNetappPolicyWorkerRestarts(t, f.netappPolicyFixture, 2, f.finishPatch)
}

func TestNetappBackupPolicyWaitsBeforeBothUpdates(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(wantError bool) contracts.WaitResult {
		t.Helper()
		wire, _ := json.Marshal(result)
		if json.Unmarshal(wire, &result) != nil {
			t.Fatal("receipt")
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		driver, err = fresh.ResolveAction(t.Context(), "connection", req.Asset)
		if err != nil {
			t.Fatal(err)
		}
		req.ExecutionResult = &result
		out, err := driver.Wait(t.Context(), req, result)
		if (err != nil) != wantError {
			t.Fatal("wait", err)
		}
		if err == nil {
			result.Data = out.Data
		}
		return out
	}
	for _, id := range f.volumes {
		for phase := 0; phase < 2; phase++ {
			f.status[id] = "Transferring"
			if out := wait(false); out.Done || f.patches[id] != phase {
				t.Fatal("transfer did not wait")
			}
			f.status[id] = "Unknown"
			wait(true)
			f.status[id] = "Idle"
			f.failAfterMutation = true
			wait(false)
			if result.Data["operation"] == nil || f.patches[id] != phase+1 {
				t.Fatal("accepted receipt missing")
			}
			wait(true) // own-read failure after saved acceptance cannot replay PATCH
			delete(f.readFault, id)
			if out := wait(false); out.Done || f.patches[id] != phase+1 {
				t.Fatal("lagging own read did not wait")
			}
			f.finishPatch(id)
			wait(false)
		}
	}
	wait(false)
	if f.policyDeletes != 1 {
		t.Fatal("missing DELETE")
	}
	wait(false)
	wait(true)
	delete(f.readFault, f.policy)
	if out := wait(false); out.Done {
		t.Fatal("live policy closed")
	}
	f.missing[f.policy] = true
	if out := wait(false); !out.Done {
		t.Fatal("absent policy not closed")
	}
	for _, id := range f.volumes {
		if f.patches[id] != 2 || f.missing[id] {
			t.Fatal("volume was not retained")
		}
	}
}

func TestNetappBackupPolicyChangesBlockMutation(t *testing.T) {
	for _, fault := range []string{"policy uuid", "schedule", "volume uuid", "pool uuid", "vault", "snapshot policy", "unknown setting", "protected", "parent missing", "volume missing", "new consumer", "missing impact", "delete impact", "forged proof"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappBackupPolicyFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			id := f.volumes[0]
			p := object(f.objects[id]["properties"])
			switch fault {
			case "policy uuid":
				object(f.objects[f.policy]["properties"])["backupPolicyId"] = testApplication
			case "schedule":
				object(f.objects[f.policy]["properties"])["dailyBackupsToKeep"] = 99
			case "volume uuid":
				p["fileSystemId"] = testTenant
			case "pool uuid":
				object(f.objects[f.id]["properties"])["poolId"] = testTenant
			case "vault":
				object(object(p["dataProtection"])["backup"])["backupVaultId"] = redisParentID(f.policy) + "/backupvaults/other"
			case "snapshot policy":
				object(object(p["dataProtection"])["snapshot"])["snapshotPolicyId"] = ""
			case "unknown setting":
				p["futureWritableProperty"] = true
			case "protected":
				f.objects[id]["tags"] = map[string]any{"steward:protected": "true"}
			case "parent missing":
				f.missing[redisParentID(f.policy)] = true
			case "volume missing":
				f.missing[id] = true
			case "new consumer":
				v := batchClone(f.objects[id])
				target := redisParentID(id) + "/volumes/new"
				v["id"], v["name"] = target, "new"
				f.objects[target] = v
			case "missing impact":
				req.LifecycleImpacts = req.LifecycleImpacts[1:]
			case "delete impact":
				req.LifecycleImpacts[0].Delete = true
			case "forged proof":
				req.Asset.Normalized[netappAssignmentProof] = "forged"
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || len(f.patches) != 0 || f.policyDeletes != 0 {
				t.Fatal("changed boundary accepted", err)
			}
		})
	}
}

func TestNetappBackupPolicySuspendedAndResumed(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
	for _, id := range f.volumes {
		object(object(object(f.objects[id]["properties"])["dataProtection"])["backup"])["policyEnforced"] = false
	}
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || out.Data["paused"] != true || len(f.patches) != 0 {
		t.Fatal("already suspended policy", err, out)
	}
	result.Data = out.Data
	id := f.volumes[0]
	object(object(object(f.objects[id]["properties"])["dataProtection"])["backup"])["policyEnforced"] = true
	if _, err := driver.Wait(t.Context(), req, result); err == nil || len(f.patches) != 0 {
		t.Fatal("externally resumed policy detached")
	}
}

func TestNetappBackupPolicyWithoutAssignments(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
	for _, id := range f.volumes {
		f.pending[id] = "policyEnforced"
		f.finishPatch(id)
		f.pending[id] = "backupPolicyId"
		f.finishPatch(id)
	}
	req := f.request(t)
	req.LifecycleImpacts = nil
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || len(f.patches) != 0 || f.policyDeletes != 1 {
		t.Fatal("empty policy deletion", out, err)
	}
}

func TestNetappBackupPolicyAsyncUpdates(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
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
				t.Fatal("fixture did not validate PATCH")
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
		wire, _ := json.Marshal(result)
		if json.Unmarshal(wire, &result) != nil {
			t.Fatal("receipt")
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		driver, err = fresh.ResolveAction(t.Context(), "connection", req.Asset)
		if err != nil {
			t.Fatal(err)
		}
		req.ExecutionResult = &result
		out, err := driver.Wait(t.Context(), req, result)
		if err != nil || out.Done {
			t.Fatal("update wait", out, err)
		}
		result.Data = out.Data
	}
	for _, id := range f.volumes {
		for stage := 0; stage < 2; stage++ {
			wait()
			wait()
			if object(result.Data["operation"])["status_done"] != true {
				t.Fatal("callback checkpoint missing")
			}
			wait() // Location completed, own resource still has its old fields.
			if result.Data["operation"] == nil || f.patches[id] != stage+1 {
				t.Fatal("callback replaced own read")
			}
			f.finishPatch(id)
			wait()
		}
	}
	wait()
	if f.policyDeletes != 1 {
		t.Fatal("policy not deleted after four persisted updates")
	}
}

func TestNetappBackupPolicyNeedsNativeIncarnation(t *testing.T) {
	f := newNetappBackupPolicyFixture(t)
	delete(object(f.objects[f.policy]["properties"]), "backupPolicyId")
	req := f.request(t)
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset); err == nil {
		t.Fatal("missing native policy UUID accepted")
	}
}

func TestNetappBackupPolicyRecordedIdentity(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/backup-policy-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var recording struct {
		SourceSHA    string `json:"source_sha256"`
		Interactions []struct {
			Method, URL, Body string
			Status            int
		}
	}
	if json.Unmarshal(wire, &recording) != nil || recording.SourceSHA != "1485ba9eeb12e8ca420e3b0ff19a9095676f7e5c3af9349838aa1f4ec754c60f" || len(recording.Interactions) != 3 {
		t.Fatal("native policy recording provenance")
	}
	for _, row := range recording.Interactions {
		if row.Method != "GET" || row.Status != 200 || !strings.HasSuffix(row.URL, "?api-version="+netappVersion) {
			t.Fatal("wrong native policy operation")
		}
		var raw map[string]any
		if json.Unmarshal([]byte(row.Body), &raw) != nil {
			t.Fatal("native policy body")
		}
		p := object(raw["properties"])
		if p["provisioningState"] != "Succeeded" || !uuidPattern.MatchString(text(p["backupPolicyId"])) {
			t.Fatal("missing native policy incarnation")
		}
		f := newNetappBackupPolicyFixture(t)
		// The replay retains native identity/configuration fields, adapting only
		// membership count to the independently reviewed two-volume fixture.
		p["volumesAssigned"] = 2
		f.objects[f.policy]["properties"] = p
		req := f.request(t)
		driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Execute(t.Context(), req); err != nil {
			t.Fatal("native policy rejected", err)
		}
		p["backupPolicyId"] = testApplication
		if _, err := driver.Execute(t.Context(), req); err == nil {
			t.Fatal("recreated native policy accepted")
		}
	}
}
