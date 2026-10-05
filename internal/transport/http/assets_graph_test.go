package httptransport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAssetGraphIncludesLifecycleAndValidatesDepth(t *testing.T) {
	_, router := terminalRouter(t)
	get := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/assets/asset-a/graph?connection_id=connection-a"+query, nil)
		request.Header.Set("Authorization", "Bearer viewer-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	response := get("&depth=1&include=lifecycle")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var combined map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &combined); err != nil ||
		string(combined["relationships"]) != "[]" || string(combined["bindings"]) != "[]" {
		t.Fatalf("combined body=%s err=%v", response.Body.String(), err)
	}

	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(get("").Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	if _, exists := legacy["bindings"]; exists {
		t.Fatalf("legacy graph response gained bindings: %v", legacy)
	}

	for _, depth := range []string{"0", "4", "x"} {
		if response := get("&depth=" + depth); response.Code != http.StatusBadRequest {
			t.Fatalf("depth=%s status=%d", depth, response.Code)
		}
	}
}
