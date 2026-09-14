package gcp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const bigtableTestHost = "bigtableadmin.googleapis.com"
const bigtableTestKind = bigtableTestHost + "/Table"
const bigtableTestParent = "projects/sample-project/instances/wide"
const bigtableTestName = bigtableTestParent + "/tables/events"

// Native LIST exposes replication only; GET metadata depends on the requested
// view. Deliberately omit schema/protection fields from the list response.
func bigtableTestData(name string) map[string]any {
	return map[string]any{"name": name, "deletionProtection": true, "columnFamilies": map[string]any{"events": map[string]any{"gcRule": map[string]any{"maxNumVersions": 1}}}, "clusterStates": map[string]any{"zone-a": map[string]any{"replicationState": "READY"}}, "granularity": "MILLIS", "automatedBackupPolicy": map[string]any{"retentionPeriod": "604800s", "frequency": "86400s"}}
}

func TestBigtableInventoryFullDetailsAcrossPages(t *testing.T) {
	reads := map[string]int{}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != bigtableTestHost {
			t.Fatalf("unexpected request %s", req.URL)
		}
		respond := func(data any) (*http.Response, error) { return dataformResponse(req, 200, data), nil }
		if req.URL.Path == "/v2/projects/sample-project/instances" {
			return respond(map[string]any{"instances": []any{map[string]any{"name": bigtableTestParent}, map[string]any{"name": "projects/sample-project/instances/archive"}}})
		}
		if strings.HasSuffix(req.URL.Path, "/tables") {
			if req.URL.Query().Get("view") != "REPLICATION_VIEW" || req.URL.Query().Get("pageSize") != "100" {
				t.Fatal("invalid list view/page size", req.URL)
			}
			table, token := "events", "next"
			if req.URL.Query().Get("pageToken") == "next" {
				table, token = "history", ""
			}
			name := strings.TrimPrefix(req.URL.Path, "/v2/") + "/" + table
			return respond(map[string]any{"tables": []any{map[string]any{"name": name, "clusterStates": bigtableTestData(name)["clusterStates"]}}, "nextPageToken": token})
		}
		if strings.Contains(req.URL.Path, "/tables/") {
			if req.URL.Query().Get("view") != "FULL" {
				return respond(map[string]any{"name": strings.TrimPrefix(req.URL.Path, "/v2/")})
			}
			reads[req.URL.Path]++
			// Project number aliases are accepted, but the table and instance stay bound.
			return respond(bigtableTestData(strings.Replace(strings.TrimPrefix(req.URL.Path, "/v2/"), "sample-project", "123456", 1)))
		}
		t.Fatalf("unexpected detail %s", req.URL)
		return nil, nil
	})
	request := productRequest(r, bigtableTestKind, "global")
	items := []contracts.InventoryItem{}
	for page := 0; ; page++ {
		if page == 10 {
			t.Fatal("pagination did not finish")
		}
		batch, err := r.List(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(items) != 4 || len(reads) != 4 {
		t.Fatal("missing parent/table pages or detail reads", len(items), reads)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.NativeID] || reads["/v2/"+strings.TrimPrefix(item.NativeID, "//"+bigtableTestHost+"/")] != 1 {
			t.Fatal("duplicate/wrong detail", item.NativeID, reads)
		}
		seen[item.NativeID] = true
		value := asset.Asset{Identity: asset.Identity{NativeType: bigtableTestKind, NativeID: item.NativeID}, Normalized: item.Normalized}
		assertGCPPropertyQuery(t, r, []asset.Asset{value}, bigtableTestKind, item.NativeID, `properties.deletionProtection = true AND properties.granularity = "MILLIS"`)
		if len(object(item.Normalized["columnFamilies"])) != 1 || len(object(item.Normalized["clusterStates"])) != 1 || object(item.Normalized["automatedBackupPolicy"])["frequency"] != "86400s" {
			t.Fatal("incomplete detail", item)
		}
		if object(object(item.Raw["resource"])["data"])["deletionProtection"] != true {
			t.Fatal("raw observation still contains only a list summary")
		}
		c, err := r.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		physical, err := c.infraPhysicalRead(t.Context(), bigtableTestKind, item.NativeID)
		if err != nil || infraPhysicalVisible(bigtableTestKind, physical) != infraPhysicalVisible(bigtableTestKind, item.Normalized) || protectionReason(bigtableTestKind, physical) != "deletion_protection_enabled" {
			t.Fatal("Infrastructure Manager lost full inventory metadata/protection", physical, err)
		}
	}
}

func TestBigtableDetailFailureCannotCompleteInventory(t *testing.T) {
	for _, failure := range []string{"403", "404", "empty", "missing name", "foreign project", "sibling instance", "different table", "list sibling", "list missing name"} {
		t.Run(failure, func(t *testing.T) {
			reads := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != bigtableTestHost {
					t.Fatalf("unexpected request %s", req.URL)
				}
				switch req.URL.Path {
				case "/v2/projects/sample-project/instances":
					return dataformResponse(req, 200, map[string]any{"instances": []any{map[string]any{"name": bigtableTestParent}}}), nil
				case "/v2/" + bigtableTestParent + "/tables":
					name := bigtableTestName
					if failure == "list sibling" {
						name = strings.Replace(name, "/wide/", "/other/", 1)
					}
					if failure == "list missing name" {
						name = ""
					}
					return dataformResponse(req, 200, map[string]any{"tables": []any{map[string]any{"name": name}}}), nil
				case "/v2/" + bigtableTestName:
					reads++
					if req.URL.Query().Get("view") != "FULL" {
						t.Fatal("incomplete detail request", req.URL)
					}
					if failure == "403" {
						return dataformResponse(req, 403, map[string]any{}), nil
					}
					if failure == "404" {
						return dataformResponse(req, 404, map[string]any{}), nil
					}
					data := bigtableTestData(bigtableTestName)
					switch failure {
					case "empty":
						data = map[string]any{}
					case "missing name":
						delete(data, "name")
					case "foreign project":
						data["name"] = strings.Replace(bigtableTestName, "sample-project", "foreign-project", 1)
					case "sibling instance":
						data["name"] = strings.Replace(bigtableTestName, "/wide/", "/other/", 1)
					case "different table":
						data["name"] = bigtableTestParent + "/tables/other"
					}
					return dataformResponse(req, 200, data), nil
				}
				t.Fatalf("unreviewed detail request %s", req.URL)
				return nil, nil
			})
			batch, err := r.List(t.Context(), productRequest(r, bigtableTestKind, "global"))
			if err == nil || batch.Complete || len(batch.Items) != 0 || (strings.HasPrefix(failure, "list ") && reads != 0) || (!strings.HasPrefix(failure, "list ") && reads != 1) {
				t.Fatal("bad detail established inventory", reads, batch, err)
			}
		})
	}
}

func TestBigtableTableCleanupUsesFullBoundRead(t *testing.T) {
	for _, failure := range []string{"protected", "changed name", "403", "delete"} {
		t.Run(failure, func(t *testing.T) {
			writes, reads := 0, 0
			gone := false
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != bigtableTestHost || req.URL.Path != "/v2/"+bigtableTestName {
					t.Fatalf("foreign request %s", req.URL)
				}
				if req.Method == "DELETE" {
					if req.URL.RawQuery != "" {
						t.Fatal("read view leaked into DELETE", req.URL)
					}
					writes++
					gone = true
					return dataformResponse(req, 200, map[string]any{}), nil
				}
				if req.Method != "GET" || req.URL.Query().Get("view") != "FULL" {
					t.Fatal("incomplete action read", req.URL)
				}
				reads++
				if gone {
					return dataformResponse(req, 404, map[string]any{}), nil
				}
				if failure == "403" {
					return dataformResponse(req, 403, map[string]any{}), nil
				}
				data := bigtableTestData(bigtableTestName)
				if failure == "changed name" {
					data["name"] = bigtableTestParent + "/tables/other"
					data["deletionProtection"] = false
				}
				if failure == "delete" {
					data["deletionProtection"] = false
				}
				return dataformResponse(req, 200, data), nil
			})
			value := asset.Asset{ID: "table", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: bigtableTestKind, NativeID: "//" + bigtableTestHost + "/" + bigtableTestName}, Normalized: map[string]any{"name": bigtableTestName}}
			driver, err := r.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			check, err := driver.Preflight(t.Context(), request)
			if failure == "protected" && (err != nil || check.Allowed || check.Reason != "deletion_protection_enabled") {
				t.Fatal(check, err)
			}
			if (failure == "changed name" || failure == "403") && err == nil {
				t.Fatal("unbound/denied read allowed", check)
			}
			result, err := driver.Execute(t.Context(), request)
			if failure != "delete" {
				if err == nil || writes != 0 {
					t.Fatal("unsafe write", writes, err)
				}
				if failure == "changed name" || failure == "403" {
					if _, err := driver.Readback(t.Context(), request); err == nil {
						t.Fatal("failed readback trusted")
					}
				}
			} else {
				if err != nil || writes != 1 {
					t.Fatal("delete failed", writes, err)
				}
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || !wait.Done {
					t.Fatal("absence not verified", wait, err)
				}
				if _, err = driver.Execute(t.Context(), request); err != nil || writes != 1 {
					t.Fatal("already absent delete repeated", writes, err)
				}
			}
			if reads < 2 {
				t.Fatal("execution did not refresh metadata")
			}
		})
	}
}
