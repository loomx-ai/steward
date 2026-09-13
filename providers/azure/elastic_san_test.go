package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func elasticSanTestRecord(kind string) map[string]any {
	id := strings.ToLower(resourceID(elasticSanType, "san"))
	if kind == elasticSanGroupType || kind == elasticSanVolumeType || kind == elasticSanSnapshotType {
		id += "/volumegroups/group"
	}
	if kind == elasticSanVolumeType {
		id += "/volumes/volume"
	} else if kind == elasticSanSnapshotType {
		id += "/snapshots/snapshot"
	} else if kind == elasticSanEndpointType {
		id += "/privateendpointconnections/endpoint"
	}
	raw := map[string]any{"id": id, "type": kind, "name": last(id), "properties": map[string]any{"provisioningState": "Succeeded"}}
	if kind == elasticSanType {
		raw["location"] = "eastus"
	}
	return raw
}

func TestElasticSanNativeReadAndCollections(t *testing.T) {
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		t.Run(kind, func(t *testing.T) {
			raw := elasticSanTestRecord(kind)
			id := text(raw["id"])
			parent := elasticSanParent(id, kind)
			collection := parent + "/" + last(kind)
			if parent == "" {
				collection = "/subscriptions/" + testSubscription + "/providers/" + kind
			}
			calls, expectedHeader := 0, ""
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Query().Get("api-version") != elasticSanVersion || req.Header.Get("x-ms-access-soft-deleted-resources") != expectedHeader {
					t.Fatal("native request changed", req.Method, req.URL.Path)
				}
				if strings.EqualFold(req.URL.Path, id) {
					return jsonResponse(200, raw, nil), nil
				}
				if !strings.EqualFold(req.URL.Path, collection) {
					t.Fatal("wrong collection", req.URL.Path)
				}
				if req.URL.Query().Get("$skiptoken") == "next" {
					return jsonResponse(200, map[string]any{"value": []any{}}, http.Header{"X-Ms-Request-Id": {"last-page"}}), nil
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": apiURL(collection, elasticSanVersion) + "&%24skiptoken=next"}, nil), nil
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.elasticSanRead(t.Context(), id, kind)
			if err != nil || res.data["id"] != id {
				t.Fatal(res, err)
			}
			modes := []bool{false}
			if kind == elasticSanGroupType || kind == elasticSanVolumeType {
				modes = append(modes, true)
			}
			for _, retained := range modes {
				if kind == elasticSanGroupType || kind == elasticSanVolumeType {
					expectedHeader = fmt.Sprint(retained)
				}
				rows, requestID, err := c.elasticSanIndex(t.Context(), kind, parent, retained)
				if err != nil || len(rows) != 1 || requestID != "last-page" || object(rows[0])["id"] != id {
					t.Fatal(rows, requestID, err)
				}
			}
			if calls != 1+2*len(modes) {
				t.Fatal("missing native pages", calls)
			}
		})
	}
}

func TestElasticSanCollectionFailuresAreAtomic(t *testing.T) {
	for _, failure := range []string{"foreign-subscription", "foreign-parent", "foreign-host", "relative", "encoded-path", "version", "duplicate-version", "filter", "empty-token", "duplicate-token", "malformed-query", "cycle", "reordered-cycle", "missing-value", "null-value", "object-value", "nonstring-link", "accepted", "polling", "error", "forbidden", "parent-missing", "duplicate-record", "foreign-record", "bad-type", "bad-name", "bad-properties", "bad-capacity", "bad-tags", "bad-policy", "bad-volume-id"} {
		t.Run(failure, func(t *testing.T) {
			raw := elasticSanTestRecord(elasticSanVolumeType)
			parent := elasticSanParent(text(raw["id"]), elasticSanVolumeType)
			collection := parent + "/volumes"
			endpoint := apiURL(collection, elasticSanVersion)
			calls := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("x-ms-access-soft-deleted-resources") != "true" || calls > 2 {
					t.Fatal("selector lost or loop escaped", calls)
				}
				body := map[string]any{"value": []any{raw}}
				status, headers := 200, http.Header{}
				switch failure {
				case "foreign-subscription":
					body["nextLink"] = strings.Replace(endpoint, testSubscription, testTenant, 1)
				case "foreign-parent":
					body["nextLink"] = strings.Replace(endpoint, "/group/", "/other/", 1)
				case "foreign-host":
					body["nextLink"] = strings.Replace(endpoint, "management.azure.com", "evil.invalid", 1)
				case "relative":
					body["nextLink"] = collection
				case "encoded-path":
					body["nextLink"] = strings.Replace(endpoint, "/group/", "/%67roup/", 1)
				case "version":
					body["nextLink"] = strings.Replace(endpoint, elasticSanVersion, "2025-09-01", 1)
				case "duplicate-version":
					body["nextLink"] = endpoint + "&api-version=" + elasticSanVersion
				case "filter":
					body["nextLink"] = endpoint + "&$filter=active"
				case "empty-token":
					body["nextLink"] = endpoint + "&$skiptoken="
				case "duplicate-token":
					body["nextLink"] = endpoint + "&$skiptoken=a&$skiptoken=b"
				case "malformed-query":
					body["nextLink"] = endpoint + "&$skiptoken=%GG"
				case "cycle":
					body["nextLink"] = endpoint
				case "reordered-cycle":
					body["value"] = []any{}
					body["nextLink"] = endpoint + "&%24skiptoken=next"
					if calls == 2 {
						body["nextLink"] = "https://management.azure.com" + strings.ToUpper(collection) + "?$skiptoken=next&api-version=" + elasticSanVersion
					}
				case "missing-value":
					delete(body, "value")
				case "null-value":
					body["value"] = nil
				case "object-value":
					body["value"] = map[string]any{}
				case "nonstring-link":
					body["nextLink"] = 1
				case "accepted":
					status = 202
				case "polling":
					headers.Set("Azure-AsyncOperation", endpoint)
				case "error":
					body["error"] = map[string]any{"code": "Incomplete"}
				case "forbidden":
					status = 403
				case "parent-missing":
					status = 404
				case "duplicate-record":
					body["nextLink"] = endpoint + "&$skiptoken=next"
				case "foreign-record":
					raw["id"] = strings.Replace(text(raw["id"]), "/group/", "/other/", 1)
				case "bad-type":
					raw["type"] = elasticSanSnapshotType
				case "bad-name":
					raw["name"] = "different"
				case "bad-properties":
					raw["properties"] = []any{}
				case "bad-capacity":
					object(raw["properties"])["sizeGiB"] = 1.5
				case "bad-tags":
					raw["tags"] = map[string]any{"tag": 2}
				case "bad-policy":
					object(raw["properties"])["deleteRetentionPolicy"] = map[string]any{"retentionPeriodDays": -1}
				case "bad-volume-id":
					object(raw["properties"])["volumeId"] = " " + testTenant
				}
				return jsonResponse(status, body, headers), nil
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			rows, provenance, err := c.elasticSanIndex(t.Context(), elasticSanVolumeType, parent, true)
			if err == nil || rows != nil || provenance != "" || calls == 0 {
				t.Fatal("incomplete collection escaped", rows, provenance, calls, err)
			}
		})
	}
}

func TestElasticSanIdentityAndReadFailures(t *testing.T) {
	raw := elasticSanTestRecord(elasticSanVolumeType)
	id := text(raw["id"])
	for _, failure := range []string{"not-found", "accepted", "polling", "wrong-id", "wrong-type", "wrong-name", "missing-properties"} {
		t.Run(failure, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("x-ms-access-soft-deleted-resources") != "" {
					t.Fatal("undeclared GET selector")
				}
				body, status, headers := maps.Clone(raw), 200, http.Header{}
				switch failure {
				case "not-found":
					status = 404
				case "accepted":
					status = 202
				case "polling":
					headers.Set("Location", apiURL(id, elasticSanVersion))
				case "wrong-id":
					body["id"] = id + "-other"
					body["name"] = last(id) + "-other"
				case "wrong-type":
					body["type"] = elasticSanGroupType
				case "wrong-name":
					body["name"] = "different"
				case "missing-properties":
					delete(body, "properties")
				}
				return jsonResponse(status, body, headers), nil
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.elasticSanRead(t.Context(), id, elasticSanVolumeType)
			if err == nil || failure == "not-found" && !isNotFound(err) {
				t.Fatal("GET failure lost", err)
			}
		})
	}
	c := &client{subscription: testSubscription}
	for _, bad := range []string{" " + id, id + " ", id + "/", strings.Replace(id, testSubscription, testTenant, 1), id + "?query=1", strings.Replace(id, "/volumes/", "/snapshots/", 1), strings.Replace(id, "/providers/", "/providers/microsoft.foo/parents/p/providers/", 1)} {
		if _, err := c.elasticSanIdentity(bad, elasticSanVolumeType); err == nil {
			t.Fatal("invalid native identity accepted", bad)
		}
	}
	for _, kind := range []string{"", elasticSanType, elasticSanEndpointType, elasticSanSnapshotType} {
		if _, _, err := c.elasticSanIndex(t.Context(), kind, "", true); err == nil {
			t.Fatal("unsupported retained collection accepted", kind)
		}
	}
}

func TestElasticSanOriginalSoftDeleteRecording(t *testing.T) {
	var manifest struct {
		SourceURI    string `json:"source_uri"`
		SourceSHA256 string `json:"source_sha256"`
		Responses    []struct {
			File, SHA256, Method, Path, Retained string
			Version                              string `json:"api_version"`
			Interaction, Status                  int
		} `json:"responses"`
	}
	data, err := os.ReadFile("fixtures/elastic-san/cli-soft-delete-sources.json")
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.SourceSHA256 != "0e5497b7b9decbd6e03ac4b2e5c8fd546855242682efaf876c4149bb3d170f03" || !strings.Contains(manifest.SourceURI, "/2aa1d8fc6417d0d5055acd88e9491e29734ad62b/") || len(manifest.Responses) != 10 {
		t.Fatal("original recording provenance", err)
	}
	observed := map[int][]any{}
	for _, entry := range manifest.Responses {
		data, err := os.ReadFile("fixtures/elastic-san/" + entry.File)
		body := map[string]any{}
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != entry.SHA256 || json.Unmarshal(data, &body) != nil || entry.Version != "2024-07-01-preview" || entry.Method != "GET" || entry.Status != 200 || !slices.Contains([]string{"true", "false"}, entry.Retained) {
			t.Fatal("original response changed", entry.File, err)
		}
		kind := elasticSanGroupType
		if strings.HasSuffix(entry.Path, "/volumes") {
			kind = elasticSanVolumeType
		}
		parent := strings.ToLower(entry.Path[:strings.LastIndex(entry.Path, "/")])
		// Replay unchanged older-preview response bytes through current transport.
		// This establishes recorded identities, not 2026 service compatibility.
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			if !strings.EqualFold(req.URL.Path, entry.Path) || req.URL.Query().Get("api-version") != elasticSanVersion || req.Header.Get("x-ms-access-soft-deleted-resources") != entry.Retained {
				t.Fatal("recorded collection selection changed")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
		})
		c, err := r.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		c.subscription = strings.Split(parent, "/")[2]
		rows, _, err := c.elasticSanIndex(t.Context(), kind, parent, entry.Retained == "true")
		if err != nil {
			t.Fatal(entry.File, err)
		}
		observed[entry.Interaction] = rows
	}
	if len(observed[23]) != 2 || len(observed[27]) != 1 || len(observed[28]) != 1 || len(observed[35]) != 2 || len(observed[38]) != 1 || len(observed[39]) != 1 || len(observed[42]) != 2 || len(observed[43]) != 0 || len(observed[48]) != 1 || len(observed[49]) != 0 {
		t.Fatal("recorded active/retained populations changed")
	}
	retained := object(observed[39][0])
	retainedProps := object(retained["properties"])
	matched := false
	for _, value := range observed[35] {
		active := object(value)
		if object(active["properties"])["volumeId"] == retainedProps["volumeId"] {
			if active["id"] == retained["id"] || retained["name"] != text(active["name"])+"-1751081600" || retainedProps["provisioningState"] != "Deleted" {
				t.Fatal("retained native rename evidence changed")
			}
			matched = true
		}
	}
	if !matched {
		t.Fatal("stable native volumeId missing")
	}
}

func TestElasticSanPublicSKUAndZones(t *testing.T) {
	raw := elasticSanTestRecord(elasticSanType)
	props := object(raw["properties"])
	props["sku"] = map[string]any{"name": "Premium_ZRS", "tier": "Premium", "future": "private-san-sku"}
	props["availabilityZones"] = []any{"1", "2"}
	props["storageTarget"] = map[string]any{"targetIqn": "private-san-target"}
	public := object(object(elasticSanSafeValue(raw))["properties"])
	encoded, _ := json.Marshal(public)
	if strings.Contains(string(encoded), "private-san-") || object(public["sku"])["name"] != "Premium_ZRS" || !slices.Equal(stringValues(public["availabilityZones"]), []string{"1", "2"}) {
		t.Fatal("public operational metadata", public)
	}
	if !nativeConfigurationContains(elasticSanSafeValue(raw), elasticSanSafeValue(elasticSanSafeValue(raw))) {
		t.Fatal("repeated projection lost operational metadata")
	}
	props["availabilityZones"] = []any{"1", map[string]any{"secret": "private-san-zone"}}
	if object(object(elasticSanSafeValue(raw))["properties"])["availabilityZones"] != nil {
		t.Fatal("malformed zones partially published")
	}
}

func TestElasticSanMalformedOperationalMetadata(t *testing.T) {
	c := &client{subscription: testSubscription}
	for key, values := range map[string][]any{
		"provisioningState":                 {true, 1, []any{}},
		"volumeId":                          {"bad", " " + testTenant, true},
		"sizeGiB":                           {"1", -1, 1.5, true, 1e30},
		"encryptionInTransit":               {"true", 1},
		"sku":                               {"Premium_LRS", map[string]any{"name": 1}, map[string]any{"tier": false}},
		"availabilityZones":                 {"1", []any{"1", 2}},
		"groupIds":                          {"group", []any{"group", true}},
		"privateLinkServiceConnectionState": {"Approved", map[string]any{"status": true}, map[string]any{"actionsRequired": 1}},
		"deleteRetentionPolicy":             {"Enabled", map[string]any{"policyState": true}, map[string]any{"retentionPeriodDays": "7"}, map[string]any{"retentionPeriodDays": -1}, map[string]any{"retentionPeriodDays": 2147483648}},
	} {
		for _, value := range values {
			raw := elasticSanTestRecord(elasticSanVolumeType)
			object(raw["properties"])[key] = value
			if _, err := c.elasticSanRecord(raw, elasticSanVolumeType); err == nil {
				t.Fatal("malformed operational field accepted", key, value)
			}
		}
	}
	for _, value := range []any{nil, "", " eastus", 1} {
		raw := elasticSanTestRecord(elasticSanType)
		raw["location"] = value
		if _, err := c.elasticSanRecord(raw, elasticSanType); err == nil {
			t.Fatal("invalid SAN location accepted", value)
		}
	}
}
