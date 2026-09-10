package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type insightsLegacyActionFixture struct {
	client                      *client
	parent, child, group        map[string]any
	locks                       []any
	id, parentID, groupID, kind string
	gone, parentGone, hold      bool
	deletes, childReads         int
	deleteStatus                int
	deleteHeaders               http.Header
	deleteBody                  any
	overrideBody                bool
	before                      func(*http.Request)
}

func newInsightsLegacyActionFixture(t *testing.T, kind string) (*insightsLegacyAction, contracts.ActionRequest, *insightsLegacyActionFixture) {
	t.Helper()
	row := insightsLegacyKind(kind)
	selector := "OpaqueID"
	if kind == insightsExportType {
		selector = "uGOoki0jQsyEs3IdQ83Q4QsNr4="
	}
	f := &insightsLegacyActionFixture{kind: kind, parent: nativeResource(applicationInsightsType, "App", "eastus", map[string]any{"AppId": "one", "CreationDate": "2026-09-01T00:00:00Z"}), child: map[string]any{row.field: selector, "Content": "private query", "Config": "private favorite", "ConfigProperties": "private work item", "Properties": "private annotation"}, locks: []any{}, deleteStatus: 200}
	f.parentID, _, _ = parseID(text(f.parent["id"]))
	f.groupID = strings.Join(strings.Split(f.parentID, "/")[:5], "/")
	f.group = map[string]any{"id": f.groupID, "name": "test", "type": groupType, "location": "eastus"}
	var err error
	f.id, err = insightsLegacyURL(f.parentID, kind, selector)
	if err != nil {
		t.Fatal(err)
	}
	f.client = directClient(func(r *http.Request) (*http.Response, error) {
		if f.before != nil {
			f.before(r)
		}
		if r.Method == "GET" && strings.EqualFold(r.URL.Path, f.parentID) {
			if f.parentGone {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, f.parent, nil), nil
		}
		if r.Method == "GET" && strings.EqualFold(r.URL.Path, f.groupID) {
			return jsonResponse(200, f.group, nil), nil
		}
		if r.Method == "GET" && strings.EqualFold(r.URL.Path, f.client.root()+"/providers/Microsoft.Authorization/locks") {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		if err := f.client.insightsLegacyEndpoint(r.URL.String(), f.id); err != nil {
			t.Fatal("action escaped native endpoint", err)
		}
		if r.Method == "GET" {
			f.childReads++
			if f.gone {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			if kind == insightsAnnotationType {
				return jsonResponse(200, []any{f.child}, nil), nil
			}
			return jsonResponse(200, f.child, nil), nil
		}
		if r.Method != "DELETE" {
			t.Fatal("unexpected mutation", r.Method)
		}
		if r.Header.Get("x-ms-client-request-id") != azureRequestID("reviewed-delete") || r.Header.Get("If-Match") != "" {
			t.Fatal("invented condition or lost request identity")
		}
		f.deletes++
		if !f.hold && f.deleteStatus == 200 {
			f.gone = true
		}
		body := f.deleteBody
		if !f.overrideBody && kind == insightsExportType {
			body = f.child
		}
		if body == nil {
			return &http.Response{StatusCode: f.deleteStatus, Header: f.deleteHeaders, Body: http.NoBody}, nil
		}
		return jsonResponse(f.deleteStatus, body, f.deleteHeaders), nil
	})
	value := asset.Asset{ID: "legacy-asset", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: kind, NativeID: f.id}, Location: "eastus", Normalized: map[string]any{"_insights_component": f.parentID, "_insights_component_configuration": f.client.privateConfiguration(monitorPrivateLinkTargetSnapshot(f.parent)), "_insights_legacy_private_configuration": f.client.insightsLegacyConfiguration(f.id, kind, f.child)}}
	a, err := newInsightsLegacyAction(f.client, "connection", value, insightsLegacyTestKind(kind))
	if err != nil {
		t.Fatal(err)
	}
	return a, contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "reviewed-delete"}, f
}

func TestApplicationInsightsLegacyActionLifecycle(t *testing.T) {
	for _, kind := range []string{insightsAnalyticsType, insightsMyAnalyticsType, insightsExportType, insightsFavoriteType, insightsWorkItemType, insightsAnnotationType} {
		t.Run(kind, func(t *testing.T) {
			a, request, f := newInsightsLegacyActionFixture(t, kind)
			ctx := context.Background()
			if check, err := a.Preflight(ctx, request); err != nil || !check.Allowed || check.Absent {
				t.Fatal("review failed", check, err)
			}
			f.hold = true
			result, err := a.Execute(ctx, request)
			if err != nil || f.deletes != 1 || result.ProviderOperationID != "" {
				t.Fatal("native delete failed", result, err)
			}
			payload, err := json.Marshal(result)
			if err != nil || strings.Contains(string(payload), "private ") {
				t.Fatal("private content escaped receipt")
			}
			var persisted contracts.ActionResult
			if json.Unmarshal(payload, &persisted) != nil {
				t.Fatal("receipt did not persist")
			}
			restarted, err := newInsightsLegacyAction(f.client, "connection", request.Asset, insightsLegacyTestKind(kind))
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := restarted.Wait(ctx, request, persisted); err != nil || wait.Done {
				t.Fatal("HTTP success hid surviving resource", wait, err)
			}
			f.gone = true
			if wait, err := restarted.Wait(ctx, request, persisted); err != nil || !wait.Done {
				t.Fatal("native absence failed", wait, err)
			}
			request.ExecutionResult = &persisted
			if read, err := restarted.Readback(ctx, request); err != nil || read.Exists {
				t.Fatal("final absence failed", read, err)
			}
			if _, err := a.Execute(ctx, request); err != nil || f.deletes != 1 {
				t.Fatal("already absent resource was deleted twice", err)
			}
			f.gone = false
			f.child["ConfigProperties"] = "replacement"
			if _, err := restarted.Wait(ctx, request, persisted); err == nil {
				t.Fatal("replacement was treated as the old resource")
			}
		})
	}
}

func TestApplicationInsightsLegacyActionDriftAndProtection(t *testing.T) {
	for _, scenario := range []string{"child-content", "parent-content", "parent-missing", "proof-missing", "parent-proof-missing", "parent-identity", "parent-location", "managed-group", "group-tag", "lock", "final-read-drift"} {
		t.Run(scenario, func(t *testing.T) {
			a, request, f := newInsightsLegacyActionFixture(t, insightsWorkItemType)
			switch scenario {
			case "child-content":
				f.child["ConfigProperties"] = "changed"
			case "parent-content":
				object(f.parent["properties"])["AppId"] = "changed"
			case "parent-missing":
				f.parentGone = true
			case "proof-missing":
				delete(request.Asset.Normalized, "_insights_legacy_private_configuration")
			case "parent-proof-missing":
				delete(request.Asset.Normalized, "_insights_component_configuration")
			case "parent-identity":
				request.Asset.Normalized["_insights_component"] = strings.Replace(f.parentID, "/app", "/other", 1)
			case "parent-location":
				f.parent["location"] = "westus"
			case "managed-group":
				f.group["managedBy"] = resourceID(applicationInsightsType, "Controller")
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": f.parentID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "final-read-drift":
				f.before = func(r *http.Request) {
					if r.Method == "GET" && f.childReads == 1 && strings.Contains(r.URL.Path, "/WorkItemConfigs/") {
						f.child["ConfigProperties"] = "changed after lock read"
					}
				}
			}
			if _, err := a.Execute(context.Background(), request); err == nil || f.deletes != 0 {
				t.Fatal("unreviewed mutation", scenario, err, f.deletes)
			}
		})
	}
}

func TestApplicationInsightsLegacyActionIdentityAndReceipt(t *testing.T) {
	a, request, f := newInsightsLegacyActionFixture(t, insightsAnalyticsType)
	result, err := a.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"provider", "connection", "partition", "kind", "id", "location", "parameters", "impacts", "prerequisites", "receipt", "operation", "credential"} {
		t.Run(scenario, func(t *testing.T) {
			changed := request
			changed.Asset.Normalized = maps.Clone(request.Asset.Normalized)
			receipt := result
			receipt.Data = maps.Clone(result.Data)
			driver := *a
			switch scenario {
			case "provider":
				changed.Asset.Identity.Provider = asset.ProviderGCP
			case "connection":
				changed.Asset.Identity.ConnectionID = "other"
			case "partition":
				changed.Asset.Identity.Partition = "other"
			case "kind":
				changed.Asset.Identity.NativeType = insightsMyAnalyticsType
			case "id":
				changed.Asset.Identity.NativeID = strings.Replace(a.id, "OpaqueID", "opaqueid", 1)
			case "location":
				changed.Asset.Location = "westus"
			case "parameters":
				changed.Parameters = map[string]any{"id": "other"}
			case "impacts":
				changed.LifecycleImpacts = []contracts.ActionImpact{{Asset: request.Asset, Delete: true}}
			case "prerequisites":
				changed.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: request.Asset, Delete: true}}
			case "receipt":
				receipt.Data["_insights_legacy_receipt"] = "forged"
			case "operation":
				receipt.ProviderOperationID = apiURL(f.parentID, insightsComponentVersion)
			case "credential":
				other := *f.client
				other.fingerprint[0] ^= 1
				driver.client = &other
			}
			before := f.childReads
			if _, err := driver.Wait(context.Background(), changed, receipt); err == nil {
				t.Fatal("changed request/receipt accepted")
			}
			changed.ExecutionResult = &receipt
			if _, err := driver.Readback(context.Background(), changed); err == nil {
				t.Fatal("changed readback accepted")
			}
			if f.childReads != before {
				t.Fatal("invalid identity reached native read")
			}
		})
	}
	// Reusing a real proof for the same opaque ID under another parent or
	// native scope cannot authorize deletion of that other resource.
	other := request.Asset
	other.Normalized = maps.Clone(other.Normalized)
	other.Identity.NativeType = insightsMyAnalyticsType
	other.Identity.NativeID, _ = insightsLegacyURL(a.parent, insightsMyAnalyticsType, "OpaqueID")
	if f.client.insightsLegacyConfiguration(other.Identity.NativeID, other.Identity.NativeType, f.child) == text(other.Normalized["_insights_legacy_private_configuration"]) {
		t.Fatal("private proof was not bound to its native scope")
	}
	if _, err := newInsightsLegacyAction(f.client, "other", request.Asset, a.kind); err == nil {
		t.Fatal("resolver accepted another connection")
	}
}

func TestApplicationInsightsLegacyDeleteResponseBoundaries(t *testing.T) {
	for _, kind := range []string{insightsWorkItemType, insightsExportType} {
		for _, scenario := range []string{"202", "204", "operation-header", "wrong-body", "wrong-id", "changed-export"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				a, request, f := newInsightsLegacyActionFixture(t, kind)
				switch scenario {
				case "202":
					f.deleteStatus = 202
				case "204":
					f.deleteStatus = 204
				case "operation-header":
					f.deleteHeaders = http.Header{"Location": []string{apiURL(f.parentID+"/operations/unknown", insightsLegacyVersion)}}
				case "wrong-body":
					f.overrideBody = true
					f.deleteBody = map[string]any{"unexpected": true}
				case "wrong-id":
					f.overrideBody = true
					f.deleteBody = map[string]any{"ExportId": "other"}
				case "changed-export":
					f.overrideBody = true
					f.deleteBody = maps.Clone(f.child)
					object(f.deleteBody)["ConfigProperties"] = "unreviewed"
				}
				if _, err := a.Execute(context.Background(), request); err == nil {
					t.Fatal("invalid native delete success accepted", scenario)
				}
			})
		}
	}
}

func TestApplicationInsightsLegacyAbsentParentStillChecksChild(t *testing.T) {
	a, request, f := newInsightsLegacyActionFixture(t, insightsWorkItemType)
	f.parentGone = true
	if _, err := a.Readback(context.Background(), request); err == nil || f.childReads != 1 {
		t.Fatal("parent absence hid surviving child", err)
	}
	f.gone = true
	if read, err := a.Readback(context.Background(), request); err != nil || read.Exists || f.childReads != 2 {
		t.Fatal("exact child absence did not complete", read, err)
	}
}

func TestApplicationInsightsLegacyRecordedDeleteBodies(t *testing.T) {
	payload, err := os.ReadFile("fixtures/applicationinsights/cli-recordings.json")
	var files []struct {
		File    string `json:"file"`
		Records []struct {
			Method string `json:"method"`
			Status int    `json:"status"`
			Body   any    `json:"body"`
		} `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &files) != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		kind := ""
		switch file.File {
		case "test_appinsights_component_favorite.yaml":
			kind = insightsFavoriteType
		case "test_component_continues_export.yaml":
			kind = insightsExportType
		default:
			continue
		}
		row := insightsLegacyKind(kind)
		var lastRead map[string]any
		for _, record := range file.Records {
			if record.Method == "GET" && record.Status == 200 && object(record.Body)[row.field] != nil {
				lastRead = object(record.Body)
			}
			if record.Method == "GET" && record.Status == 200 {
				for _, value := range array(record.Body) {
					if current := object(value); lastRead != nil && current[row.field] == lastRead[row.field] {
						// The export CLI scenario updates its destination between
						// its last item GET and final LIST. Preserve that reviewed
						// update instead of suppressing configuration differences.
						lastRead = current
					}
				}
			}
			if record.Method != "DELETE" {
				continue
			}
			if lastRead == nil {
				t.Fatal("native DELETE has no preceding GET")
			}
			_, request, f := newInsightsLegacyActionFixture(t, kind)
			// Compose the last original GET/LIST object and DELETE body without
			// schema repair. Parent reads and the final 404 are local fixtures;
			// this is not a recording of the full Steward action lifecycle.
			f.child = lastRead
			f.id, err = insightsLegacyURL(f.parentID, kind, text(lastRead[row.field]))
			if err != nil {
				t.Fatal(err)
			}
			request.Asset.Identity.NativeID = f.id
			request.Asset.Normalized["_insights_legacy_private_configuration"] = f.client.insightsLegacyConfiguration(f.id, kind, f.child)
			f.overrideBody, f.deleteBody, f.deleteStatus = true, record.Body, record.Status
			driver, err := newInsightsLegacyAction(f.client, "connection", request.Asset, insightsLegacyTestKind(kind))
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || f.deletes != 1 {
				t.Fatal("recorded native DELETE failed", file.File, err)
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
				t.Fatal("recorded deletion did not confirm absence", wait, err)
			}
			checked++
		}
	}
	if checked != 2 {
		t.Fatal("native DELETE recording coverage changed", checked)
	}
}
