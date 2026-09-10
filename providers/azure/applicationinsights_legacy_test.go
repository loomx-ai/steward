package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func insightsLegacyTestKind(kind string) resourceType {
	row := insightsLegacyKind(kind)
	return resourceType{NativeType: row.kind, ReadOperations: []string{insightsLegacyOperationID(row.read)}, DeleteOperations: []string{insightsLegacyOperationID(row.delete)}}
}

func TestApplicationInsightsLegacyNativeIdentities(t *testing.T) {
	parent, _, _ := parseID(resourceID(applicationInsightsType, "Component"))
	for _, test := range []struct{ kind, file string }{
		{insightsAnalyticsType, "AnalyticsItemGet.json"}, {insightsMyAnalyticsType, "AnalyticsItemGet.json"},
		{insightsExportType, "ExportConfigurationGet.json"}, {insightsFavoriteType, "FavoriteGet.json"},
		{insightsWorkItemType, "WorkItemConfigGet.json"}, {insightsAnnotationType, "AnnotationsGet.json"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/applicationinsights/stable/2015-05-01/examples/" + test.file)
			var example struct {
				Parameters map[string]any `json:"parameters"`
				Responses  map[string]struct {
					Body any `json:"body"`
				} `json:"responses"`
			}
			if err != nil || json.Unmarshal(payload, &example) != nil {
				t.Fatal(err)
			}
			row := insightsLegacyKind(test.kind)
			selector := text(example.Parameters[row.parameter])
			id, err := insightsLegacyURL(parent, test.kind, selector)
			if err != nil {
				t.Fatal(err)
			}
			parsed, parsedParent, parsedKind, parsedSelector, err := insightsLegacyIdentity(id)
			if err != nil || parsed != id || parsedParent != parent || parsedKind != row.kind || parsedSelector != selector {
				t.Fatal("native selector did not survive URL identity", id, err)
			}
			calls := 0
			c := directClient(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Query().Get("api-version") != insightsLegacyVersion {
					t.Fatal("native read changed")
				}
				if row.parameter == "id" {
					if r.URL.Query().Get("id") != selector || !strings.HasSuffix(r.URL.Path, "/"+row.collection+"/item") || r.URL.Query().Has("name") {
						t.Fatal("analytics query identity became a path or name selector")
					}
				} else if last(r.URL.Path) != selector {
					t.Fatal("native opaque path changed", r.URL.Path)
				}
				return jsonResponse(200, example.Responses["200"].Body, nil), nil
			})
			result, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(test.kind), id)
			if err != nil || calls != 1 || result.data[row.field] != selector {
				t.Fatal("native read failed", err)
			}
			for _, method := range []string{"GET", "DELETE"} {
				op, params, err := c.resourceOperation(insightsLegacyTestKind(test.kind), id, method)
				if err != nil {
					t.Fatal(err)
				}
				bound, err := bindAzureREST(op, params)
				if err != nil || bound.Method != method || c.insightsLegacyEndpoint(bound.URL, id) != nil {
					t.Fatal("native operation identity changed", err)
				}
			}
		})
	}
}

func TestApplicationInsightsLegacyOpaqueSelectors(t *testing.T) {
	parent := resourceID(applicationInsightsType, "Component")
	for _, test := range []struct{ kind, selector string }{
		{insightsExportType, "uGOoki0jQsyEs3IdQ83Q4QsNr4="},
		{insightsExportType, "/aB+C/dE=="},
		{insightsAnalyticsType, "Opaque%ID&name=other/+?#"},
		{insightsMyAnalyticsType, "OpaqueID"},
		{insightsWorkItemType, "Visual Studio Team Services"},
		{insightsFavoriteType, "Favourite-中文"},
	} {
		t.Run(test.kind+"/"+test.selector, func(t *testing.T) {
			id, err := insightsLegacyURL(parent, test.kind, test.selector)
			if err != nil {
				t.Fatal(err)
			}
			canonical, gotParent, kind, selector, err := insightsLegacyIdentity(id)
			if err != nil || canonical != id || !strings.EqualFold(gotParent, parent) || kind != test.kind || selector != test.selector {
				t.Fatal("lost opaque identity", err)
			}
			// Canonicalize only ARM and endpoint spelling. The opaque value and
			// its native scope continue to distinguish resources in the graph.
			alias := strings.Replace(id, "management.azure.com", "MANAGEMENT.AZURE.COM", 1)
			alias = strings.Replace(alias, gotParent, strings.ToUpper(gotParent), 1)
			if parsed, _, _, _, err := insightsLegacyIdentity(alias); err != nil || parsed != id {
				t.Fatal("ARM casing was not canonicalized", err)
			}
			first := asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: kind, NativeID: id}
			otherID, err := insightsLegacyURL(parent, kind, strings.ToLower(selector))
			if err != nil {
				t.Fatal(err)
			}
			other := first
			other.NativeID = otherID
			if id != otherID && first.Key() == other.Key() {
				t.Fatal("opaque case variants collided")
			}
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("id") != "" && r.URL.Query().Get("id") != selector {
					t.Fatal("query selector changed")
				}
				if strings.Contains(selector, "/") && test.kind == insightsExportType && !strings.HasSuffix(r.URL.EscapedPath(), "/"+url.PathEscape(selector)) {
					t.Fatal("opaque slash became a path boundary")
				}
				return jsonResponse(200, map[string]any{insightsLegacyKind(kind).field: selector}, nil), nil
			})
			if _, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(kind), id); err != nil {
				t.Fatal(err)
			}
		})
	}
	// ARM resource groups may contain Unicode; URL escaping must not make a
	// legitimate parent look like a different subscription or resource.
	unicodeParent := strings.Replace(parent, "/resourceGroups/test/", "/resourceGroups/资源组/", 1)
	id, err := insightsLegacyURL(unicodeParent, insightsWorkItemType, "Visual Studio")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := insightsLegacyIdentity(id); err != nil {
		t.Fatal("escaped ARM parent failed", err)
	}
}

func TestApplicationInsightsLegacyIdentityBoundaries(t *testing.T) {
	parent, _, _ := parseID(resourceID(applicationInsightsType, "Component"))
	base := armOrigin + parent
	valid := base + "/analyticsitems/item?id=OpaqueID"
	for _, invalid := range []string{
		base + "/analyticsitems/OpaqueID", base + "/analyticsitems/item", base + "/analyticsitems/item?id=", valid + "&id=other", valid + "&name=other", valid + "&api-version=2015-05-01", valid + "&x=1", valid + ";x=1", valid + "#", valid + "#fragment", " " + valid,
		strings.Replace(valid, "https://", "http://", 1), strings.Replace(valid, "management.azure.com", "management.azure.com.evil", 1), strings.Replace(valid, "management.azure.com", "user@management.azure.com", 1), strings.Replace(valid, "management.azure.com", "management.azure.com:443", 1),
		strings.Replace(valid, "/components/component/", "/components/other%2Fcomponent/", 1), strings.Replace(valid, "/components/", "/workspaces/", 1), strings.Replace(valid, "/analyticsitems/", "/unknown/", 1),
		base + "/exportconfiguration/..", base + "/exportconfiguration/x%2F..%2Fy", base + "/exportconfiguration/a%252Fb", base + "/exportconfiguration/x?", base + "/workitemconfigs/a%2Fb", base + "/favorites/%00", base + "/annotations/%ff",
	} {
		if _, _, _, _, err := insightsLegacyIdentity(invalid); err == nil {
			t.Fatal("unsafe identity accepted", invalid)
		}
	}
	calls := 0
	c := directClient(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(200, map[string]any{}, nil), nil
	})
	kind := insightsLegacyTestKind(insightsAnalyticsType)
	for _, wrong := range []resourceType{
		insightsLegacyTestKind(insightsMyAnalyticsType), insightsLegacyTestKind(insightsExportType),
		{NativeType: insightsAnalyticsType, ReadOperations: []string{insightsOperationPrefix + "Components_Get"}},
		{NativeType: insightsAnalyticsType, ReadOperations: []string{insightsOperationPrefix + "AnalyticsItems_Get", insightsOperationPrefix + "AnalyticsItems_Get"}},
	} {
		if _, err := c.insightsLegacyRead(context.Background(), wrong, valid); err == nil {
			t.Fatal("wrong kind/catalog binding accepted")
		}
	}
	foreign := strings.Replace(valid, testSubscription, "22222222-2222-4222-8222-222222222222", 1)
	if _, err := c.insightsLegacyRead(context.Background(), kind, foreign); err == nil {
		t.Fatal("foreign subscription accepted")
	}
	if _, _, err := c.resourceOperation(kind, valid, "POST"); err == nil {
		t.Fatal("mutation method substitution accepted")
	}
	for _, endpoint := range []string{valid, valid + "&api-version=2020-01-01", valid + "&api-version=2015-05-01&api-version=2015-05-01", valid + "&api-version=2015-05-01&name=other", strings.Replace(valid, "OpaqueID", "opaqueid", 1) + "&api-version=2015-05-01", valid + "&api-version=2015-05-01#"} {
		if c.insightsLegacyEndpoint(endpoint, valid) == nil {
			t.Fatal("changed request accepted", endpoint)
		}
	}
	if calls != 0 {
		t.Fatal("invalid identity reached transport")
	}
}

func TestApplicationInsightsLegacyResponseBoundaries(t *testing.T) {
	parent := resourceID(applicationInsightsType, "Component")
	for _, kind := range []string{insightsAnalyticsType, insightsMyAnalyticsType, insightsExportType, insightsFavoriteType, insightsWorkItemType, insightsAnnotationType} {
		row := insightsLegacyKind(kind)
		id, _ := insightsLegacyURL(parent, kind, "OpaqueID")
		for _, body := range []map[string]any{{}, {row.field: nil}, {row.field: 1}, {row.field: "opaqueid"}, {row.field: "OpaqueID", strings.ToLower(row.field): "OpaqueID"}} {
			var native any = body
			if kind == insightsAnnotationType {
				native = []any{body}
			}
			c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(200, native, nil), nil })
			if _, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(kind), id); err == nil {
				t.Fatal("unbound response accepted", kind, body)
			}
		}
		for _, status := range []int{201, 202, 204} {
			var body any = map[string]any{row.field: "OpaqueID"}
			if kind == insightsAnnotationType {
				body = []any{body}
			}
			c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(status, body, nil), nil })
			if _, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(kind), id); err == nil {
				t.Fatal("non-GET success accepted", status)
			}
		}
		c := directClient(func(*http.Request) (*http.Response, error) {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), nil
		})
		if _, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(kind), id); !isNotFound(err) {
			t.Fatal("native absence lost", err)
		}
	}
	id, _ := insightsLegacyURL(parent, insightsAnnotationType, "OpaqueID")
	for _, body := range []any{[]any{}, []any{map[string]any{"Id": "OpaqueID"}, map[string]any{"Id": "OpaqueID"}}, map[string]any{"value": []any{map[string]any{"Id": "OpaqueID"}}}} {
		c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(200, body, nil), nil })
		if _, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(insightsAnnotationType), id); err == nil {
			t.Fatal("annotation response did not identify exactly one native object")
		}
	}
}

func TestApplicationInsightsLegacyPrivateSnapshot(t *testing.T) {
	for _, kind := range []string{insightsAnalyticsType, insightsFavoriteType, insightsWorkItemType, insightsAnnotationType} {
		row := insightsLegacyKind(kind)
		raw := map[string]any{row.field: "OpaqueID", "Content": "secret", "Config": "secret", "ConfigProperties": "secret", "Properties": "secret", "TimeModified": "one"}
		snapshot := insightsLegacySnapshot(kind, raw)
		if !reflect.DeepEqual(snapshot, raw) {
			t.Fatal("private configuration was omitted")
		}
		delete(snapshot, row.field)
		if raw[row.field] != "OpaqueID" {
			t.Fatal("snapshot mutated source")
		}
	}
	raw := map[string]any{"ExportId": "OpaqueID", "DestinationAccountId": "one", "LastUserUpdate": "one", "ExportStatus": "Preparing", "LastSuccessTime": "one", "LastGapTime": "one", "PermanentErrorReason": "none"}
	changed := maps.Clone(raw)
	changed["ExportStatus"], changed["LastSuccessTime"] = "Running", "two"
	if !reflect.DeepEqual(insightsLegacySnapshot(insightsExportType, raw), insightsLegacySnapshot(insightsExportType, changed)) {
		t.Fatal("export progress changed configuration")
	}
	changed["DestinationAccountId"] = "two"
	if reflect.DeepEqual(insightsLegacySnapshot(insightsExportType, raw), insightsLegacySnapshot(insightsExportType, changed)) {
		t.Fatal("destination change was lost")
	}
}

func TestApplicationInsightsOpaqueExportBindingBoundaries(t *testing.T) {
	data, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ExportConfigurations_Get", "ExportConfigurations_Delete"} {
		op, _ := data.catalog.Operation(insightsOperationPrefix + name)
		params := map[string]any{"subscriptionId": testSubscription, "resourceGroupName": "test", "resourceName": "app", "exportId": "A/B+=="}
		request, err := bindAzureREST(op, params)
		if err != nil {
			t.Fatal("native opaque export failed", err)
		}
		u, err := url.Parse(request.URL)
		if err != nil || !strings.HasSuffix(u.EscapedPath(), "/"+url.PathEscape("A/B+==")) {
			t.Fatal("export ID lost its single encoded segment", request.URL, err)
		}
		for _, mutation := range []string{"name", "version", "path", "method"} {
			changed := op
			call := *op.Call
			changed.Call = &call
			switch mutation {
			case "name":
				changed.ID = insightsOperationPrefix + "Unknown_Get"
			case "version":
				changed.Call.Version = "2020-02-02"
			case "path":
				changed.Call.Path = strings.Replace(call.Path, "/exportconfiguration/", "/other/", 1)
			case "method":
				changed.Call.Method = "POST"
			}
			if _, err := bindAzureREST(changed, params); err == nil {
				t.Fatal("opaque export exception escaped native binding", mutation)
			}
		}
		for _, invalid := range []string{"a/../b", "a/./b", "a%2Fb", "a\\b", "a\x00b", "a\nb", "a\rb"} {
			params["exportId"] = invalid
			if _, err := bindAzureREST(op, params); err == nil {
				t.Fatal("unsafe export path accepted", invalid)
			}
		}
	}
}
