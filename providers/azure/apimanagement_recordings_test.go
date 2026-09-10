package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func apimRecordings(t *testing.T) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/apimanagement/cli-recordings.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "9ed235982b164e0493dac6f756084aa998991026e60a11178d59ebd6d9b61a91" {
		t.Fatal("APIM recording evidence changed", err)
	}
	// The account identity is composed with the local fixture credential. Bodies
	// and headers otherwise retain the native evidence in the checked-in file.
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		URI     string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 1 || len(sources[0].Records) != 67 || sources[0].SHA != "ffdbb745bd5fcd9e5736578e4f5ba1571d1bd75d13bc844b5b7b8ae5710c7774" || !strings.Contains(sources[0].URI, "/8bead7f93f086629efb160d56c25f508156925bf/") {
		t.Fatal("invalid native APIM provenance")
	}
	rows := map[int]redisRecordedResponse{}
	for _, row := range sources[0].Records {
		rows[row.Index] = row
	}
	return rows
}

// The recording used 2022-08-01. Exercise its native response shapes against
// selected 2024-05-01 routes; change only the polling header's version parameter.
func apimRecordedHTTP(row redisRecordedResponse) *http.Response {
	res := streamAnalyticsRecordedHTTP(row)
	for _, key := range []string{"Location", "Azure-AsyncOperation"} {
		for i, value := range res.Header.Values(key) {
			res.Header[http.CanonicalHeaderKey(key)][i] = strings.ReplaceAll(value, "api-version=2022-08-01", "api-version="+apimVersion)
		}
	}
	return res
}

func apimRecordedScenario(t *testing.T, rows map[int]redisRecordedResponse, indices []int) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.apimanagement/gateways"] = []any{}
	s.version["/subscriptions/"+testSubscription+"/providers/microsoft.apimanagement/gateways"] = apimVersion
	var bodies []map[string]any
	reads := map[string]redisRecordedResponse{}
	for _, index := range indices {
		row := rows[index]
		id, kind, err := parseID(text(row.Body["id"]))
		if err != nil || !isAPIMType(kind) {
			t.Fatal("invalid recorded resource", index, err)
		}
		res := apimRecordedHTTP(row)
		raw := maps.Clone(row.Body)
		raw["_apim_header_etag"] = res.Header.Get("ETag")
		s.add(raw, apimVersion)
		reads[id] = row
		bodies = append(bodies, raw)
		for _, child := range apimOwnedKinds(kind) {
			path := id + "/" + strings.ToLower(last(child))
			s.lists[path], s.version[path] = []any{}, apimVersion
		}
		if strings.EqualFold(kind, apimServiceType) {
			s.lists[id+"/workspacelinks"], s.version[id+"/workspacelinks"] = []any{}, apimVersion
			s.lists[id+"/issues"], s.version[id+"/issues"] = []any{}, apimVersion
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "name": last(group), "type": groupType, "location": "centralus"}}
			s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.apimanagement/service"] = []any{row.Body}
		}
	}
	for _, raw := range bodies {
		id, kind, _ := parseID(text(raw["id"]))
		if !strings.EqualFold(kind, apimServiceType) {
			path := redisParentID(id) + "/" + strings.ToLower(last(kind))
			s.lists[path] = append(s.lists[path], raw)
		}
		if strings.EqualFold(last(kind), "apis") {
			base := apimBaseAPI(id)
			rev := text(object(raw["properties"])["apiRevision"])
			s.lists[base+"/revisions"] = append(s.lists[base+"/revisions"], map[string]any{"apiId": "/apis/" + last(base) + ";rev=" + rev, "apiRevision": rev, "isCurrent": id == base})
			s.version[base+"/revisions"] = apimVersion
		}
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.Method == "GET" && !s.gone[id] {
			if row, ok := reads[id]; ok {
				return apimRecordedHTTP(row), true
			}
		}
		return nil, false
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range bodies {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}

func TestAPIMRecordedNativeDeletesPollingAndFinalAbsence(t *testing.T) {
	for _, tc := range []struct {
		read, deletion int
		parents        []int
	}{
		{112, 360, nil}, {279, 308, []int{112}}, {285, 288, []int{112, 291}},
		{291, 310, []int{112}}, {297, 300, []int{112, 296}},
		{324, 329, []int{112, 319}}, {327, 328, []int{112, 319, 324}},
		{330, 333, []int{112, 319}}, {341, 349, []int{112}}, {354, 358, []int{112}},
	} {
		t.Run(fmt.Sprint(tc.deletion), func(t *testing.T) {
			rows := apimRecordings(t)
			s, r, assets := apimRecordedScenario(t, rows, append(tc.parents, tc.read))
			target := assets[len(assets)-1]
			request, _ := dnsRequest(t, r, assets, target)
			original := s.handle
			pollIndex := 361
			deletion := rows[tc.deletion]
			operationURL, _ := url.Parse(apimRecordedHTTP(deletion).Header.Get("Azure-AsyncOperation"))
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
					for _, flag := range []string{"deleteRevisions", "deleteSubscriptions", "force", "notify"} {
						if req.URL.Query().Get(flag) == "true" {
							t.Fatal("recorded forced cascade was copied into runtime", flag)
						}
					}
					if target.Identity.NativeType != apimServiceType && req.Header.Get("If-Match") != text(target.Normalized["arm_etag"]) {
						t.Fatal("recorded native header ETag was lost")
					}
					s.deletes = append(s.deletes, target.Identity.NativeID)
					return apimRecordedHTTP(deletion), true
				}
				if req.Method == "GET" && operationURL != nil && operationURL.Path != "" && strings.EqualFold(req.URL.Path, operationURL.Path) {
					res := apimRecordedHTTP(rows[pollIndex])
					if pollIndex < 367 {
						pollIndex++
					}
					return res, true
				}
				return original(req)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal("native recorded DELETE", err)
			}
			waits := 1
			if tc.deletion == 360 {
				waits = 7
			}
			for i := 0; i < waits; i++ {
				waited, err := driver.Wait(context.Background(), request, receipt)
				if err != nil || waited.Done {
					t.Fatal("recorded operation hid surviving target", i, waited.State, err)
				}
				if waited.Data != nil {
					receipt.Data = waited.Data
				}
				encoded, _ := json.Marshal(receipt)
				json.Unmarshal(encoded, &receipt)
				encoded, _ = json.Marshal(request)
				json.Unmarshal(encoded, &request)
				driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			}
			// Original CLI ends with list absence (or poll 404). This exact target
			// GET 404 is synthetic and separately proves our stronger readback.
			s.gone[target.Identity.NativeID] = true
			streamAnalyticsAfterDelete(s)
			waited, err := driver.Wait(context.Background(), request, receipt)
			if err != nil || !waited.Done || len(s.deletes) != 1 {
				t.Fatal("recorded response replay did not reconcile absence", waited.State, err)
			}
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
				t.Fatal("recorded cleanup not idempotent", err)
			}
		})
	}
}

func TestAPIMRecordedCurrentAndNonCurrentRevisionBodies(t *testing.T) {
	rows := apimRecordings(t)
	s, r, _ := apimRecordedScenario(t, rows, []int{112, 291, 293})
	base, _, _ := parseID(text(rows[291].Body["id"]))
	// Keep the actual relative apiId metadata for revision 1; compose the
	// second entry from revision 2's GET because its index was not recorded.
	listed := object(array(rows[290].Body["value"])[0])
	s.lists[base+"/revisions"][0] = listed
	batch, err := r.List(context.Background(), productRequest(r, apimAPIType))
	if err != nil || !batch.Complete || len(batch.Items) != 2 {
		t.Fatal("recorded revisions did not survive inventory", len(batch.Items), err)
	}
	for _, item := range batch.Items {
		if item.NativeID != base && item.NativeID != base+";rev=2" || item.Normalized["arm_etag"] == "" {
			t.Fatal("revision alias or native ETag lost")
		}
	}
}
