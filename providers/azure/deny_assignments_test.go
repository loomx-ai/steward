package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func denyTestBody(scope, name string) map[string]any {
	return map[string]any{"id": scope + "/providers/" + denyAssignmentType + "/" + name, "name": name, "type": denyAssignmentType, "properties": map[string]any{"scope": scope, "isSystemProtected": true, "permissions": []any{map[string]any{"actions": []any{"*/delete"}, "condition": "private-condition"}}, "principals": []any{map[string]any{"id": "private-principal"}}, "description": "private-description"}}
}

func TestDenyAssignmentNativeContracts(t *testing.T) {
	wire, err := os.ReadFile("fixtures/deny-assignments/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		URI   string `json:"source_uri"`
		SHA   string `json:"source_sha256"`
		Files map[string]struct{ URL, SHA256, Operation string }
	}
	if json.Unmarshal(wire, &manifest) != nil || manifest.SHA != "def1fbbbba2df435548d3ecbec5d4171872abcd75cfe34f3b2413f8a9a322a49" || !strings.Contains(manifest.URI, "/07a27fbba41f8597cdfe0f866fcbf9f7c37390f4/") || len(manifest.Files) != 2 {
		t.Fatal("source manifest")
	}

	source, err := os.ReadFile("catalog/source/swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var documents catalog.RESTDocumentSet
	if json.Unmarshal(source, &documents) != nil {
		t.Fatal("source JSON")
	}
	matched := false
	for _, document := range documents.Documents {
		if document.SourceURI == manifest.URI {
			matched = document.SourceSHA256 == manifest.SHA
		}
	}
	if !matched {
		t.Fatal("source fingerprint mismatch")
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for name, entry := range manifest.Files {
		wire, err := os.ReadFile("fixtures/deny-assignments/" + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(wire)) != entry.SHA256 {
			t.Fatal("native example fingerprint", err)
		}
		var example map[string]any
		if json.Unmarshal(wire, &example) != nil {
			t.Fatal("example JSON")
		}
		op, found := metadata.catalog.Operation("Azure.Microsoft.Authorization." + entry.Operation)
		if !found || op.SourceURI != manifest.URI {
			t.Fatal("pinned native operation missing")
		}
		bound, err := catalog.BindREST(op, object(example["parameters"]))
		if err != nil || bound.Method != "GET" || len(bound.Body) != 0 {
			t.Fatal(bound, err)
		}
		u, _ := url.Parse(bound.URL)
		if u.Host != "management.azure.com" || u.Query().Get("api-version") != "2022-04-01" {
			t.Fatal("native binding boundary")
		}
	}
	c := directClient(nil)
	for _, scope := range []string{c.root(), c.root() + "/resourceGroups/group", c.root() + "/providers/Microsoft.Resources/deploymentStacks/stack"} {
		for _, name := range []string{"", "native-assignment"} {
			if _, err := c.denyAssignmentRequest(scope, name); err != nil {
				t.Fatal(scope, err)
			}
		}
	}
}

func TestDenyAssignmentSnapshotReconcilesOwnReads(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/group"
	scope := group + "/providers/microsoft.compute/virtualmachines/vm"
	ancestor, member := denyTestBody(group, "ancestor"), denyTestBody(scope, "member")
	hidden := denyTestBody(scope, "hidden")
	gone := scope + "/providers/" + denyAssignmentType + "/gone"
	calls := 0
	c := directClient(func(q *http.Request) (*http.Response, error) {
		calls++
		if q.Method != "GET" || q.URL.Query().Get("$filter") != "" {
			t.Fatal("mutation or filtered index")
		}
		switch {
		case strings.HasSuffix(strings.ToLower(q.URL.Path), "/denyassignments"):
			if q.URL.Query().Get("$skiptoken") == "next" {
				return jsonResponse(200, map[string]any{"value": []any{member}}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": []any{ancestor}, "nextLink": apiURL(q.URL.Path, "2022-04-01") + "&$skiptoken=next"}, nil), nil
		case strings.EqualFold(q.URL.Path, text(ancestor["id"])):
			return jsonResponse(200, ancestor, nil), nil
		case strings.EqualFold(q.URL.Path, text(member["id"])):
			return jsonResponse(200, member, nil), nil
		case strings.EqualFold(q.URL.Path, text(hidden["id"])):
			return jsonResponse(200, hidden, nil), nil
		case strings.EqualFold(q.URL.Path, gone):
			return jsonResponse(404, map[string]any{}, nil), nil
		default:
			t.Fatal("unexpected native path", q.URL.Path)
			return nil, nil
		}
	})
	rows, absent, err := c.denyAssignmentSnapshot(t.Context(), scope, []string{gone, text(hidden["id"])})
	if err != nil || len(rows) != 3 || len(absent) != 1 || absent[0] != strings.ToLower(gone) || calls != 12 {
		t.Fatal(len(rows), absent, calls, err)
	}
}

func TestDenyAssignmentSnapshotFailures(t *testing.T) {
	for _, fault := range []string{"list_forbidden", "listed_missing", "own_forbidden", "wrong_id", "wrong_type", "wrong_scope", "foreign", "inherited", "duplicate", "async", "filter", "version", "host", "drift"} {
		t.Run(fault, func(t *testing.T) {
			scope := "/subscriptions/" + testSubscription + "/resourcegroups/group"
			body := denyTestBody(scope, "deny")
			lists, calls := 0, 0
			c := directClient(func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" {
					t.Fatal("unexpected mutation")
				}
				if strings.HasSuffix(strings.ToLower(q.URL.Path), "/denyassignments") {
					lists++
					if fault == "list_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					headers := http.Header{}
					rows := []any{body}
					next := ""
					switch fault {
					case "foreign":
						rows = []any{denyTestBody(strings.Replace(scope, testSubscription, rbacOtherSubscription, 1), "deny")}
					case "inherited":
						rows = []any{denyTestBody("/providers/Microsoft.Management/managementGroups/parent", "deny")}
					case "duplicate":
						rows = append(rows, body)
					case "async":
						headers.Set("Azure-AsyncOperation", "https://management.azure.com/operation")
					case "filter":
						next = apiURL(q.URL.Path, "2022-04-01") + "&$filter=atScope()"
					case "version":
						next = apiURL(q.URL.Path, "2018-07-01-preview")
					case "host":
						next = "https://other.example" + q.URL.RequestURI()
					}
					result := map[string]any{"value": rows}
					if next != "" {
						result["nextLink"] = next
					}
					return jsonResponse(200, result, headers), nil
				}
				own := denyTestBody(scope, "deny")
				switch fault {
				case "listed_missing":
					return jsonResponse(404, map[string]any{}, nil), nil
				case "own_forbidden":
					return jsonResponse(403, map[string]any{}, nil), nil
				case "wrong_id":
					own["id"] = text(own["id"]) + "other"
				case "wrong_type":
					own["type"] = rbacAssignmentType
				case "wrong_scope":
					object(own["properties"])["scope"] = scope + "other"
				case "drift":
					if lists > 1 {
						object(own["properties"])["description"] = "changed-private-description"
					}
				}
				return jsonResponse(200, own, nil), nil
			})
			rows, absent, err := c.denyAssignmentSnapshot(t.Context(), scope, nil)
			if err == nil || rows != nil || absent != nil {
				t.Fatal("incomplete snapshot accepted", fault, err)
			}
			if (fault == "host" || fault == "filter" || fault == "version") && calls != 1 {
				t.Fatal("untrusted continuation reached HTTP", fault, calls)
			}
		})
	}
}

func TestDenyAssignmentScopeRejectedBeforeHTTP(t *testing.T) {
	c := directClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid scope reached HTTP"); return nil, nil })
	for _, scope := range []string{"/", "/providers/Microsoft.Management/managementGroups/group", "/subscriptions/" + rbacOtherSubscription, c.root() + "/resourceGroups/../other"} {
		if _, _, err := c.denyAssignmentSnapshot(t.Context(), scope, nil); err == nil {
			t.Fatal(scope)
		}
	}
	foreign := "/subscriptions/" + rbacOtherSubscription + "/providers/" + denyAssignmentType + "/deny"
	if _, _, err := c.denyAssignmentSnapshot(t.Context(), c.root(), []string{foreign}); err == nil {
		t.Fatal("foreign hint accepted")
	}
}

func TestDenyAssignmentCosmosScopeAndPrivacy(t *testing.T) {
	scope := "/subscriptions/" + testSubscription + "/resourceGroups/group/providers/Microsoft.DocumentDB/databaseAccounts/account/sqlDatabases/MiXeD"
	for _, changeCase := range []bool{false, true} {
		calls := 0
		c := directClient(func(q *http.Request) (*http.Response, error) {
			calls++
			if !strings.Contains(q.URL.Path, "/MiXeD/") {
				t.Fatal("native source case lost")
			}
			if strings.HasSuffix(strings.ToLower(q.URL.Path), "/denyassignments") {
				if changeCase {
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": strings.Replace(apiURL(q.URL.Path, "2022-04-01"), "/MiXeD/", "/mixed/", 1) + "&$skiptoken=next"}, nil), nil
				}
				return jsonResponse(200, map[string]any{"value": []any{denyTestBody(scope, "deny")}}, nil), nil
			}
			return jsonResponse(200, denyTestBody(scope, "deny"), nil), nil
		})
		rows, _, err := c.denyAssignmentSnapshot(t.Context(), scope, nil)
		if changeCase {
			if err == nil || calls != 1 {
				t.Fatal("changed-case cursor followed", calls, err)
			}
		} else if err != nil || len(rows) != 1 {
			t.Fatal(err)
		}
	}
	raw := map[string]any{"value": []any{denyTestBody(scope, "deny")}, "nextLink": "https://management.azure.com/private?token=private-cursor", "name": map[string]any{"unexpected": "private-nested"}, "body": "private-body"}
	safe, _ := json.Marshal(safeAPIPayload(raw, apiURL(scope+"/providers/"+denyAssignmentType, "2022-04-01")))
	for _, secret := range []string{"private-principal", "private-condition", "private-description", "private-cursor", "private-nested", "private-body"} {
		if strings.Contains(string(safe), secret) {
			t.Fatal("private deny payload exposed")
		}
	}
}
