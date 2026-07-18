package httptransport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestWriteConnectionErrorSanitizesProviderMessageOutsideValidation(t *testing.T) {
	const rawMessage = `Post "https://sts.example.test/?AccessKeyId=sensitive-key&Signature=sensitive-signature": dial tcp: connection refused`
	response := httptest.NewRecorder()
	response.Header().Set(requestIDHeader, "application-request")

	writeConnectionError(response, &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category:  execution.ErrorProviderFailure,
		Code:      "ProviderTransportError",
		Message:   rawMessage,
		RequestID: "provider-request",
	}})

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, response.Body.String())
	}
	if body.Error.Details["provider_message"] != contracts.SafeProviderValidationMessage {
		t.Fatalf("provider message=%q want=%q", body.Error.Details["provider_message"], contracts.SafeProviderValidationMessage)
	}
	if strings.Contains(response.Body.String(), rawMessage) ||
		strings.Contains(response.Body.String(), "sensitive-key") ||
		strings.Contains(response.Body.String(), "sensitive-signature") {
		t.Fatalf("non-validation error exposed raw provider message: %s", response.Body.String())
	}
}
