package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
)

func TestRegionEndpointsManageLifecycleWithExplicitRoles(t *testing.T) {
	_, router := terminalRouter(t)

	viewerList := regionRequest(router, http.MethodGet, "/api/connections/connection-a/regions", "viewer-token", "")
	if viewerList.Code != http.StatusOK || !strings.Contains(viewerList.Body.String(), `"items":[]`) {
		t.Fatalf("viewer list status=%d body=%s", viewerList.Code, viewerList.Body.String())
	}
	forbidden := regionRequest(router, http.MethodPost, "/api/connections/connection-a/regions", "viewer-token", `{"region_id":"cn-hangzhou"}`)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("viewer mutation status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	created := regionRequest(router, http.MethodPost, "/api/connections/connection-a/regions", "admin-token", `{"region_id":" cn-hangzhou ","name":" 杭州 "}`)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"region_id":"cn-hangzhou"`) || !strings.Contains(created.Body.String(), `"name":"杭州"`) || strings.Contains(created.Body.String(), "access_key") || strings.Contains(created.Body.String(), "ciphertext") {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	duplicate := regionRequest(router, http.MethodPost, "/api/connections/connection-a/regions", "admin-token", `{"region_id":"cn-hangzhou"}`)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"code":"region.conflict"`) {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	searched := regionRequest(router, http.MethodGet, "/api/connections/connection-a/regions?lifecycle=active&q=%E6%9D%AD%E5%B7%9E", "viewer-token", "")
	if searched.Code != http.StatusOK || !strings.Contains(searched.Body.String(), `"region_id":"cn-hangzhou"`) {
		t.Fatalf("search status=%d body=%s", searched.Code, searched.Body.String())
	}

	renamed := regionRequest(router, http.MethodPatch, "/api/connections/connection-a/regions/cn-hangzhou", "admin-token", `{"name":"华东生产"}`)
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), `"name":"华东生产"`) {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	reset := regionRequest(router, http.MethodPatch, "/api/connections/connection-a/regions/cn-hangzhou", "admin-token", `{"reset_name":true}`)
	if reset.Code != http.StatusOK || !strings.Contains(reset.Body.String(), `"name":"cn-hangzhou"`) || strings.Contains(reset.Body.String(), `"name_override":"华东生产"`) {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	retired := regionRequest(router, http.MethodPatch, "/api/connections/connection-a/regions/cn-hangzhou", "admin-token", `{"lifecycle":"retired"}`)
	if retired.Code != http.StatusOK || !strings.Contains(retired.Body.String(), `"lifecycle":"retired"`) {
		t.Fatalf("retire status=%d body=%s", retired.Code, retired.Body.String())
	}
	retiredList := regionRequest(router, http.MethodGet, "/api/connections/connection-a/regions?lifecycle=retired", "viewer-token", "")
	if retiredList.Code != http.StatusOK || !strings.Contains(retiredList.Body.String(), `"region_id":"cn-hangzhou"`) {
		t.Fatalf("retired list status=%d body=%s", retiredList.Code, retiredList.Body.String())
	}
	activated := regionRequest(router, http.MethodPatch, "/api/connections/connection-a/regions/cn-hangzhou", "admin-token", `{"lifecycle":"active"}`)
	if activated.Code != http.StatusOK || !strings.Contains(activated.Body.String(), `"lifecycle":"active"`) {
		t.Fatalf("activate status=%d body=%s", activated.Code, activated.Body.String())
	}
	excluded := regionRequest(router, http.MethodDelete, "/api/connections/connection-a/regions/cn-hangzhou", "admin-token", "")
	if excluded.Code != http.StatusOK || !strings.Contains(excluded.Body.String(), `"lifecycle":"excluded"`) {
		t.Fatalf("exclude status=%d body=%s", excluded.Code, excluded.Body.String())
	}
	restored := regionRequest(router, http.MethodPost, "/api/connections/connection-a/regions/cn-hangzhou/restore", "admin-token", "")
	if restored.Code != http.StatusOK || !strings.Contains(restored.Body.String(), `"lifecycle":"active"`) {
		t.Fatalf("restore status=%d body=%s", restored.Code, restored.Body.String())
	}
	crossConnection := regionRequest(router, http.MethodPatch, "/api/connections/missing/regions/cn-hangzhou", "admin-token", `{"lifecycle":"retired"}`)
	if crossConnection.Code != http.StatusNotFound || !strings.Contains(crossConnection.Body.String(), `"code":"region.not_found"`) {
		t.Fatalf("cross connection status=%d body=%s", crossConnection.Code, crossConnection.Body.String())
	}
}

func TestRegionListReturnsAllItemsWithoutPagination(t *testing.T) {
	repositories, router := terminalRouter(t)
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	for _, region := range []asset.ConnectionRegion{
		{ID: "hangzhou", ConnectionID: "connection-a", RegionID: "cn-hangzhou", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "shanghai", ConnectionID: "connection-a", RegionID: "cn-shanghai", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(context.Background(), region); err != nil {
			t.Fatal(err)
		}
	}

	response := regionRequest(router, http.MethodGet, "/api/connections/connection-a/regions?limit=1&cursor=ignored", "viewer-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items      []asset.ConnectionRegion `json:"items"`
		NextCursor string                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.NextCursor != "" {
		t.Fatalf("regions=%#v next_cursor=%q body=%s", body.Items, body.NextCursor, response.Body.String())
	}
}

func TestRegionRefreshEndpointReturnsDeduplicatedDurableJob(t *testing.T) {
	_, router := terminalRouter(t)
	first := regionRequest(router, http.MethodPost, "/api/connections/connection-a/regions/refresh", "admin-token", "")
	second := regionRequest(router, http.MethodPost, "/api/connections/connection-a/regions/refresh", "admin-token", "")
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("refresh statuses=%d,%d bodies=%s %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	var firstBody, secondBody struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if firstBody.JobID == "" || secondBody.JobID != firstBody.JobID || firstBody.Status != "pending" {
		t.Fatalf("refresh jobs=%#v %#v", firstBody, secondBody)
	}
	status := regionRequest(router, http.MethodGet, "/api/jobs/"+firstBody.JobID+"?connection_id=connection-a", "viewer-token", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"id":"`+firstBody.JobID+`"`) || !strings.Contains(status.Body.String(), `"status":"pending"`) {
		t.Fatalf("job status=%d body=%s", status.Code, status.Body.String())
	}
}

func TestConnectionListReturnsRegionCountsAndLatestRefreshOnly(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	for _, region := range []asset.ConnectionRegion{
		{ID: "active", ConnectionID: "connection-a", RegionID: "cn-hangzhou", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "retired", ConnectionID: "connection-a", RegionID: "cn-qingdao", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionRetired, CreatedAt: now, UpdatedAt: now},
		{ID: "excluded", ConnectionID: "connection-a", RegionID: "cn-beijing", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionExcluded, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Jobs().Enqueue(ctx, execution.Job{ID: "refresh-complete", ConnectionID: "connection-a", Type: execution.JobRegionRefresh, Status: execution.JobSucceeded, RunAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	response := regionRequest(router, http.MethodGet, "/api/connections", "viewer-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Items []struct {
			ID                      string `json:"id"`
			ActiveRegionCount       int    `json:"active_region_count"`
			RetiredRegionCount      int    `json:"retired_region_count"`
			ExcludedRegionCount     int    `json:"excluded_region_count"`
			LastRegionRefreshStatus string `json:"last_region_refresh_status"`
			Regions                 any    `json:"regions"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ActiveRegionCount != 1 || page.Items[0].RetiredRegionCount != 1 || page.Items[0].ExcludedRegionCount != 1 || page.Items[0].LastRegionRefreshStatus != "succeeded" || page.Items[0].Regions != nil {
		t.Fatalf("page=%#v body=%s", page, response.Body.String())
	}
}

func TestRegionEndpointsRejectUnverifiedConnection(t *testing.T) {
	repositories, router := terminalRouter(t)
	connection, err := repositories.Connections().GetConnection(context.Background(), "connection-a")
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	if err := repositories.Connections().PutConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}

	response := regionRequest(router, http.MethodGet, "/api/connections/connection-a/regions", "viewer-token", "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"connection_not_validated"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func regionRequest(router http.Handler, method, target, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
