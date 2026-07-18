package httptransport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var internalUUIDV4Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

func TestNewRequestIDGeneratesUniqueUUIDV4Values(t *testing.T) {
	values := make(map[string]struct{}, 64)
	for range 64 {
		value, err := newRequestID()
		if err != nil {
			t.Fatal(err)
		}
		if !internalUUIDV4Pattern.MatchString(value) {
			t.Fatalf("request ID %q is not a lowercase UUID v4", value)
		}
		if _, exists := values[value]; exists {
			t.Fatalf("duplicate request ID %q", value)
		}
		values[value] = struct{}{}
	}
}

func TestWriteAPIErrorUsesHTTPRequestIDAndPreservesProviderRequestID(t *testing.T) {
	const httpRequestID = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	response := httptest.NewRecorder()
	response.Header().Set(requestIDHeader, httpRequestID)

	writeAPIError(response, http.StatusBadGateway, APIError{
		Code: "provider.network_query_failed", Message: "provider failed", RequestID: "provider-request",
	})

	var body errorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, response.Body.String())
	}
	if body.Error.RequestID != httpRequestID {
		t.Fatalf("error request_id=%q want=%q", body.Error.RequestID, httpRequestID)
	}
	if body.Error.Details["provider_request_id"] != "provider-request" {
		t.Fatalf("provider request ID details=%+v", body.Error.Details)
	}
}
