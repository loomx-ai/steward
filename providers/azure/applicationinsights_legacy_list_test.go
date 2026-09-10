package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func insightsLegacyTestWindow() insightsAnnotationWindow {
	end := time.Now().UTC().Add(-time.Minute)
	return insightsAnnotationWindow{Start: end.Add(-24 * time.Hour).Format(time.RFC3339Nano), End: end.Format(time.RFC3339Nano)}
}

func TestApplicationInsightsLegacyCompleteCollections(t *testing.T) {
	parent := nativeResource(applicationInsightsType, "App", "eastus", map[string]any{"AppId": "application-id", "CreationDate": "2026-09-01T00:00:00Z"})
	parentID, _, _ := parseID(text(parent["id"]))
	for _, kind := range []string{insightsAnalyticsType, insightsMyAnalyticsType, insightsExportType, insightsFavoriteType, insightsWorkItemType, insightsAnnotationType} {
		t.Run(kind, func(t *testing.T) {
			row := insightsLegacyKind(kind)
			window := insightsAnnotationWindow{}
			if kind == insightsAnnotationType {
				window = insightsLegacyTestWindow()
			}
			parents, lists, gets := 0, 0, 0
			filtersSeen := map[string]bool{}
			resources := map[string]map[string]any{}
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("inventory mutated cloud")
				}
				if strings.EqualFold(r.URL.Path, parentID) {
					parents++
					if r.URL.Query().Get("api-version") != insightsComponentVersion {
						t.Fatal("component version changed")
					}
					return jsonResponse(200, parent, nil), nil
				}
				if strings.EqualFold(r.URL.Path, parentID+"/"+row.collection) {
					lists++
					q := r.URL.Query()
					if q.Get("api-version") != insightsLegacyVersion {
						t.Fatal("list version changed")
					}
					if row.parameter == "id" && (q.Get("includeContent") != "true" || q.Has("scope") || q.Has("type")) {
						t.Fatal("analytics list was filtered or omitted content")
					}
					if kind == insightsAnnotationType && (q.Get("start") != window.Start || q.Get("end") != window.End) {
						t.Fatal("annotation window changed")
					}
					filter := q.Get("favoriteType") + "/" + q.Get("sourceType")
					if filtersSeen[filter] {
						t.Fatal("repeated collection filter")
					}
					filtersSeen[filter] = true
					var values []any
					for _, selector := range []string{fmt.Sprintf("OpaqueID%d", lists), fmt.Sprintf("opaqueid%d", lists)} {
						raw := map[string]any{row.field: selector, "Content": "private query", "Config": "private favorite", "ConfigProperties": "private work item", "Properties": "private annotation"}
						if kind == insightsFavoriteType {
							if q.Get("canFetchContent") != "true" {
								t.Fatal("favorite omitted content")
							}
							raw["FavoriteType"] = q.Get("favoriteType")
							if q.Get("sourceType") != "" {
								raw["SourceType"] = q.Get("sourceType")
							}
						}
						id, err := insightsLegacyURL(parentID, kind, selector)
						if err != nil {
							t.Fatal(err)
						}
						resources[id] = raw
						values = append(values, maps.Clone(raw))
					}
					if kind == insightsWorkItemType || kind == insightsAnnotationType {
						return jsonResponse(200, map[string]any{"value": values}, nil), nil
					}
					return jsonResponse(200, values, nil), nil
				}
				gets++
				u := *r.URL
				q := u.Query()
				q.Del("api-version")
				u.RawQuery = q.Encode()
				id, _, _, _, err := insightsLegacyIdentity(u.String())
				if err != nil || resources[id] == nil {
					t.Fatal("GET was not bound to a listed identity", err)
				}
				if kind == insightsAnnotationType {
					return jsonResponse(200, []any{resources[id]}, nil), nil
				}
				return jsonResponse(200, resources[id], nil), nil
			})
			children, err := c.insightsLegacyChildren(context.Background(), parentID, kind, window)
			if err != nil {
				t.Fatal(err)
			}
			wantLists := 1
			if kind == insightsFavoriteType {
				wantLists = 18
			}
			if parents != 2 || lists != wantLists || gets != 2*wantLists || len(children) != gets {
				t.Fatal("incomplete native collection", parents, lists, gets, len(children))
			}
			for _, child := range children {
				if child.kind != kind || child.data == nil || child.data[row.field] == "" {
					t.Fatal("native child missing")
				}
			}
		})
	}
}

func TestApplicationInsightsFavoriteFiltersCoverNativeEnum(t *testing.T) {
	data, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	op, _ := data.catalog.Operation(insightsOperationPrefix + "Favorites_List")
	props := object(op.InputSchema["properties"])
	filters, err := insightsLegacyListParameters(insightsLegacyKind(insightsFavoriteType), insightsAnnotationWindow{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, filter := range filters {
		seen[text(filter["favoriteType"])+"/"+text(filter["sourceType"])] = true
	}
	sources := append([]any{""}, array(object(props["sourceType"])["enum"])...)
	scopes := array(object(props["favoriteType"])["enum"])
	if len(sources) != 9 || len(scopes) != 2 {
		t.Fatal("native filters changed; update coverage")
	}
	for _, scope := range scopes {
		for _, source := range sources {
			key := text(scope) + "/" + text(source)
			if !seen[key] {
				t.Fatal("unscanned native favorite category", key)
			}
			delete(seen, key)
		}
	}
	if len(seen) != 0 {
		t.Fatal("invented favorite filter")
	}
}

func TestApplicationInsightsLegacyListFailureBoundaries(t *testing.T) {
	for _, scenario := range []string{"missing-list", "forbidden-list", "missing-parent", "changed-parent", "duplicate", "malformed", "changed-id", "changed-private-content", "missing-child", "continuation", "scope-mismatch", "source-mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			kind := insightsWorkItemType
			if scenario == "scope-mismatch" || scenario == "source-mismatch" {
				kind = insightsFavoriteType
			}
			row := insightsLegacyKind(kind)
			parent := nativeResource(applicationInsightsType, "App", "eastus", map[string]any{"AppId": "one"})
			parentID, _, _ := parseID(text(parent["id"]))
			parents := 0
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if strings.EqualFold(r.URL.Path, parentID) {
					parents++
					if scenario == "missing-parent" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if scenario == "changed-parent" && parents == 2 {
						object(parent["properties"])["AppId"] = "two"
					}
					return jsonResponse(200, parent, nil), nil
				}
				raw := map[string]any{row.field: "OpaqueID", "ConfigProperties": "private one", "FavoriteType": "shared"}
				if strings.EqualFold(r.URL.Path, parentID+"/"+row.collection) {
					if scenario == "missing-list" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if scenario == "forbidden-list" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					values := []any{raw}
					if scenario == "duplicate" {
						values = append(values, maps.Clone(raw))
					}
					if scenario == "malformed" {
						values = []any{nil}
					}
					if kind == insightsFavoriteType {
						return jsonResponse(200, values, nil), nil
					}
					body := map[string]any{"value": values}
					if scenario == "continuation" {
						body["nextLink"] = r.URL.String() + "&$skiptoken=next"
					}
					return jsonResponse(200, body, nil), nil
				}
				if scenario == "missing-child" {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if scenario == "changed-id" {
					raw[row.field] = "opaqueid"
				}
				if scenario == "changed-private-content" {
					raw["ConfigProperties"] = "private two"
				}
				if scenario == "scope-mismatch" {
					raw["FavoriteType"] = "user"
				}
				if scenario == "source-mismatch" {
					raw["SourceType"] = "notebook"
				}
				return jsonResponse(200, raw, nil), nil
			})
			if children, err := c.insightsLegacyChildren(context.Background(), parentID, kind, insightsAnnotationWindow{}); err == nil || len(children) != 0 {
				t.Fatal("incomplete/changed inventory accepted", scenario, err)
			}
		})
	}
}

func TestApplicationInsightsAnnotationWindowIsBounded(t *testing.T) {
	now := time.Now().UTC()
	for _, window := range []insightsAnnotationWindow{
		{}, {Start: "bad", End: now.Format(time.RFC3339Nano)},
		{Start: now.Add(-91 * 24 * time.Hour).Format(time.RFC3339Nano), End: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		{Start: now.Add(-time.Hour).Format(time.RFC3339Nano), End: now.Add(time.Hour).Format(time.RFC3339Nano)},
		{Start: now.Add(-time.Hour).Format(time.RFC3339Nano), End: now.Add(-2 * time.Hour).Format(time.RFC3339Nano)},
	} {
		if _, err := insightsLegacyListParameters(insightsLegacyKind(insightsAnnotationType), window); err == nil {
			t.Fatal("unbounded/invalid annotation window accepted")
		}
	}
	if _, err := insightsLegacyListParameters(insightsLegacyKind(insightsAnalyticsType), insightsLegacyTestWindow()); err == nil {
		t.Fatal("unrelated collection accepted time filter")
	}
}

func TestApplicationInsightsLegacyRecordedReads(t *testing.T) {
	payload, err := os.ReadFile("fixtures/applicationinsights/cli-recordings.json")
	var files []struct {
		File    string `json:"file"`
		Records []struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
			Status int    `json:"status"`
			Body   any    `json:"body"`
		} `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &files) != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range files {
		if !slices.Contains([]string{"test_appinsights_component_favorite.yaml", "test_component_continues_export.yaml"}, file.File) {
			continue
		}
		for _, record := range file.Records {
			if record.Method != "GET" {
				continue
			}
			u, err := url.Parse(record.URI)
			if err != nil {
				t.Fatal(err)
			}
			query := u.Query()
			query.Del("api-version")
			u.RawQuery = query.Encode()
			id, parent, kind, _, err := insightsLegacyIdentity(u.String())
			if err != nil {
				continue
			} // Native LIST records are exercised separately.
			c := directClient(func(r *http.Request) (*http.Response, error) {
				// Canonical ARM casing and equivalent URI escaping may differ.
				// The decoded opaque ID and every query parameter must agree.
				original, _ := url.Parse(record.URI)
				if r.Method != record.Method || !strings.EqualFold(strings.Join(strings.Split(r.URL.Path, "/")[:10], "/"), strings.Join(strings.Split(original.Path, "/")[:10], "/")) || last(r.URL.Path) != last(original.Path) || r.URL.RawQuery != original.RawQuery {
					t.Fatal("recorded native selector changed")
				}
				return jsonResponse(record.Status, record.Body, nil), nil
			})
			c.subscription = strings.Split(parent, "/")[2]
			if _, err := c.insightsLegacyRead(context.Background(), insightsLegacyTestKind(kind), id); err != nil {
				t.Fatal(file.File, err)
			}
			count++
		}
	}
	if count != 4 {
		t.Fatal("recorded legacy GET coverage changed", count)
	}
}
