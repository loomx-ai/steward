package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestScanChangesListByTypeAndCountOnScan(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region-changes", ConnectionID: "connection-a", RegionID: "cn-hangzhou", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{"scope_mode":"selected_regions","region_ids":["cn-hangzhou"]}`))
	createRequest.Header.Set("Authorization", "Bearer operator-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var task inventory.ScanTaskProjection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &task); err != nil || createResponse.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}
	for _, change := range []asset.AssetChange{
		{ID: "chg-a", ConnectionID: "connection-a", ScanTaskID: task.ID, AssetID: "asset-a", Type: asset.ChangeAdded, ResourceKindID: "kind", NativeID: "i-a", ChangedAt: now},
		{ID: "chg-b", ConnectionID: "connection-a", ScanTaskID: task.ID, AssetID: "asset-b", Type: asset.ChangeModified, ResourceKindID: "kind", NativeID: "i-b", ChangedAt: now, Fields: []asset.FieldChange{{Path: "state", Before: "Running", After: "Stopped"}}},
	} {
		if err := repositories.Inventory().RecordAssetChange(ctx, change); err != nil {
			t.Fatal(err)
		}
	}

	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer viewer-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	response := get("/api/scans/" + string(task.ID) + "/changes?connection_id=connection-a&change_type=modified")
	var page persistence.Page[asset.AssetChange]
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != "chg-b" {
		t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	if response := get("/api/scans/" + string(task.ID) + "/changes?connection_id=connection-a&change_type=renamed"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid change type status=%d", response.Code)
	}
	response = get("/api/scans/" + string(task.ID) + "?connection_id=connection-a")
	var detail inventory.ScanTaskProjection
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || detail.Changes != (asset.ChangeCounts{Added: 1, Modified: 1}) {
		t.Fatalf("detail changes=%+v body=%s err=%v", detail.Changes, response.Body.String(), err)
	}
	response = get("/api/scans?connection_id=connection-a")
	var list persistence.Page[inventory.ScanTaskProjection]
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Changes != (asset.ChangeCounts{Added: 1, Modified: 1}) {
		t.Fatalf("list body=%s err=%v", response.Body.String(), err)
	}
}
