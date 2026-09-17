package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const osLoginUserPath = "/v1/users/steward@sample-project.iam.gserviceaccount.com"

func osLoginKeyData(fingerprint, key string) map[string]any {
	return map[string]any{"name": "users/steward@sample-project.iam.gserviceaccount.com/sshPublicKeys/" + fingerprint, "fingerprint": fingerprint, "key": key, "expirationTimeUsec": "1893456000000000"}
}

type osLoginFixture struct {
	keys    map[string]map[string]any
	deletes int
	calls   []string
}

func (f *osLoginFixture) handler(t *testing.T) roundTripFunc {
	return func(request *http.Request) (*http.Response, error) {
		f.calls = append(f.calls, request.Method+" "+request.URL.Path)
		if request.URL.Host != "oslogin.googleapis.com" {
			t.Fatalf("unexpected host %s", request.URL)
		}
		switch {
		case request.Method == "GET" && request.URL.Path == osLoginUserPath+"/loginProfile":
			payload, _ := json.Marshal(map[string]any{"name": "steward@sample-project.iam.gserviceaccount.com", "sshPublicKeys": f.keys})
			return apiResponse(request, 200, string(payload)), nil
		case strings.HasPrefix(request.URL.Path, osLoginUserPath+"/sshPublicKeys/"):
			fingerprint := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
			data, ok := f.keys[fingerprint]
			if request.Method == "DELETE" {
				f.deletes++
				delete(f.keys, fingerprint)
				if !ok {
					return apiResponse(request, 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`), nil
				}
				return apiResponse(request, 200, `{}`), nil
			}
			if !ok {
				return apiResponse(request, 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`), nil
			}
			payload, _ := json.Marshal(data)
			return apiResponse(request, 200, string(payload)), nil
		}
		t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		return nil, nil
	}
}

func osLoginRequest(r *Runtime) contracts.InventoryRequest {
	kind := r.resourceKind(osLoginKeyType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: osLoginSource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}}
}

func TestOSLoginProfileInventoryAndKnownKeyAbsence(t *testing.T) {
	fixture := &osLoginFixture{keys: map[string]map[string]any{"a1b2": osLoginKeyData("a1b2", "ssh-ed25519 AAAA laptop"), "c3d4": osLoginKeyData("c3d4", "ssh-rsa AAAB ci")}}
	r := protocolRuntime(t, fixture.handler(t))
	batch, err := r.List(context.Background(), osLoginRequest(r))
	if err != nil || !batch.Complete || len(batch.Items) != 2 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	item := batch.Items[0]
	if item.NativeID != osLoginPrefix+"users/steward@sample-project.iam.gserviceaccount.com/sshPublicKeys/a1b2" || item.Name != "a1b2" || item.Normalized["_inventory_source"] != osLoginSource || item.Normalized["project_id"] != nil || len(text(item.Normalized[osLoginReview])) != 64 {
		t.Fatalf("item = %+v", item)
	}
	// c3d4 disappears from the profile: only its own 404 closes it. A known key
	// still readable directly (eventual profile consistency) stays active.
	known := batch.Items[1].NativeID
	delete(fixture.keys, "c3d4")
	request := osLoginRequest(r)
	request.KnownNativeIDs = []string{known}
	batch, err = r.List(context.Background(), request)
	if err != nil || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != known {
		t.Fatalf("absence batch=%+v err=%v", batch, err)
	}
	request.KnownNativeIDs = []string{osLoginPrefix + "users/other@example.com/sshPublicKeys/ffff"}
	if _, err := r.List(context.Background(), request); err == nil {
		t.Fatal("another user's key must not be reconciled with this identity")
	}
}

func TestOSLoginKeyDeleteVerifiesReviewAndAbsence(t *testing.T) {
	fixture := &osLoginFixture{keys: map[string]map[string]any{"a1b2": osLoginKeyData("a1b2", "ssh-ed25519 AAAA laptop")}}
	r := protocolRuntime(t, fixture.handler(t))
	batch, err := r.List(context.Background(), osLoginRequest(r))
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	item := batch.Items[0]
	value := asset.Asset{ID: "key", Normalized: item.Normalized, Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: osLoginKeyType, NativeID: item.NativeID}}
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-key"}

	// A key re-uploaded with a different expiration is not the reviewed key.
	fixture.keys["a1b2"]["expirationTimeUsec"] = "1924992000000000"
	if check, err := driver.Preflight(context.Background(), request); err == nil || check.Allowed {
		t.Fatalf("changed key passed preflight: %+v", check)
	}
	if _, err := driver.Execute(context.Background(), request); err == nil || fixture.deletes != 0 {
		t.Fatalf("changed key was deleted (deletes=%d)", fixture.deletes)
	}
	fixture.keys["a1b2"]["expirationTimeUsec"] = "1893456000000000"

	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed || check.Absent {
		t.Fatalf("preflight=%+v err=%v", check, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || fixture.deletes != 1 || result.Data["phase"] != "oslogin_key_delete" {
		t.Fatalf("result=%+v deletes=%d err=%v", result, fixture.deletes, err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	readback, err := driver.Readback(context.Background(), request)
	if err != nil || readback.Exists {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	if _, err := r.ResolveAction(context.Background(), "another-connection", value); err == nil {
		t.Fatal("a key must be deleted through its own connection identity")
	}
	foreign := value
	foreign.Identity.NativeID = osLoginPrefix + "users/other@example.com/sshPublicKeys/a1b2"
	if _, err := r.ResolveAction(context.Background(), "connection", foreign); err == nil {
		t.Fatal("another user's key must not resolve an action")
	}
}
