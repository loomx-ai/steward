package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func cognitiveRecordings(t *testing.T, file string) map[int]redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/cognitive/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File    string                  `json:"file"`
		Source  string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 7 {
		t.Fatal("invalid Cognitive Services recordings", err)
	}
	count := 0
	result := map[int]redisRecordedResponse{}
	for _, source := range sources {
		if len(source.SHA) != 64 || !strings.Contains(source.Source, "/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/") {
			t.Fatal("unpinned Cognitive Services recording")
		}
		for _, row := range source.Records {
			count++
			if source.File == file {
				result[row.Index] = row
			}
		}
	}
	if count != 31 || len(result) == 0 {
		t.Fatal("missing Cognitive Services recorded responses", count)
	}
	return result
}
func TestCognitiveRecordedNativeDeletionRequiresReadback(t *testing.T) {
	for _, tc := range []struct {
		file                 string
		read, parent, delete int
		project              bool
	}{
		{"test_cognitiveservices_crud.yaml", 5, 5, 10, false},
		{"test_cognitiveservices_commitment_plan.yaml", 10, 5, 11, false},
		{"test_cognitiveservices_deployment.yaml", 7, 3, 8, false},
		{"test_cognitiveservices_private_endpoint_connection.yaml", 29, 22, 31, false},
		{"test_account_connections_from_file.yaml", 6, 3, 7, false},
		{"test_project_connections_from_file.yaml", 9, 3, 10, true},
		{"test_project_connections_from_file.yaml", 5, 3, 11, true},
	} {
		t.Run(tc.file+"/"+strconv.Itoa(tc.read), func(t *testing.T) {
			rows := cognitiveRecordings(t, tc.file)
			raw := rows[tc.read].Body
			root := rows[tc.parent].Body
			if tc.read == 5 && tc.project {
				raw = object(array(raw["value"])[0])
			}
			id, kind, err := parseID(text(raw["id"]))
			if err != nil {
				t.Fatal(err)
			}
			mapping, _ := findType(kind)
			kind = mapping.NativeType
			s := newDNSScenario()
			s.add(root, "2026-05-01")
			s.add(raw, "2026-05-01")
			rootID := strings.ToLower(text(root["id"]))
			group := strings.Join(strings.Split(rootID, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			// Supporting native collection reads are synthetic empty collections.
			// The unchanged upstream corpus did not exercise these account APIs.
			for _, child := range cognitiveOwnedKinds(cognitiveType) {
				s.lists[rootID+"/"+strings.ToLower(last(child))] = []any{}
			}
			s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.cognitiveservices/commitmentplans"] = []any{}
			if tc.project {
				project := object(array(rows[5].Body["value"])[0])
				projectID := strings.ToLower(text(project["id"]))
				s.add(project, "2026-05-01")
				// A recorded native LIST item supplies this supporting project GET.
				s.lists[rootID+"/projects"] = []any{project}
				for _, child := range cognitiveOwnedKinds(cognitiveProjectType) {
					s.lists[projectID+"/"+strings.ToLower(last(child))] = []any{}
				}
			}
			if kind == cognitiveDeploymentType {
				s.lists[rootID+"/deployments"] = []any{raw}
			}
			absent, deleted := false, false
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if !strings.EqualFold(req.URL.Path, id) || strings.Contains(strings.ToLower(req.URL.Path), "deletedaccounts") {
						t.Fatal("unexpected Cognitive Services deletion", req.URL.Path)
					}
					deleted = true
					response := rows[tc.delete]
					return jsonResponse(response.Status, response.Body, nil), true
				}
				if strings.EqualFold(req.URL.Path, id) && absent {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
				}
				return nil, false
			}
			r := s.runtime(t)
			target := dnsAsset(t, r, raw)
			target.Location = resourceRegion(root)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || !deleted {
				t.Fatal("recorded Cognitive Services DELETE", err)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			waited, err := driver.Wait(context.Background(), request, result)
			if err != nil || waited.Done {
				t.Fatal("native 200 DELETE completed while target exists", err)
			}
			// These CLI recordings stop at DELETE/empty LIST. A final target GET 404
			// is an explicitly synthetic contract check, not a recorded observation.
			absent = true
			waited, err = driver.Wait(context.Background(), request, result)
			if err != nil || !waited.Done {
				t.Fatal("final target absence", err)
			}
		})
	}
}
func TestCognitiveNativeSoftDeletionDoesNotAuthorizePurge(t *testing.T) {
	rows := cognitiveRecordings(t, "test_cognitiveservices_softdelete.yaml")
	deleted := rows[7]
	u, _ := url.Parse(deleted.URI)
	if !strings.Contains(strings.ToLower(u.Path), "/deletedaccounts/") || deleted.Status != 200 || text(object(deleted.Body["properties"])["deletionDate"]) == "" {
		t.Fatal("missing native soft-deletion evidence")
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := metadata.catalog.Operation("Azure.Microsoft.CognitiveServices.DeletedAccounts_Purge"); ok {
		t.Fatal("soft-delete cleanup exposed irreversible purge")
	}
}
