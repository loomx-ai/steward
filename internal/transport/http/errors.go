package httptransport

import (
	"errors"
	"log/slog"
	"net/http"

	topologyapp "github.com/loomx-ai/steward/internal/app/topology"
)

type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

type errorBody struct {
	Error APIError `json:"error"`
}

func writeError(response http.ResponseWriter, status int, err error) {
	code := "request.invalid"
	message := err.Error()
	switch status {
	case http.StatusUnauthorized:
		code = "auth.unauthenticated"
	case http.StatusForbidden:
		code = "auth.forbidden"
	case http.StatusNotFound:
		code = "resource.not_found"
	case http.StatusConflict:
		code = "resource.conflict"
	case http.StatusInternalServerError:
		code = "internal_error"
		message = "an internal server error occurred"
		logServerError(response, status, err)
	case http.StatusServiceUnavailable:
		code = "server.unavailable"
		message = "the service is temporarily unavailable"
		logServerError(response, status, err)
	}
	writeAPIError(response, status, APIError{Code: code, Message: message})
}

func logServerError(response http.ResponseWriter, status int, err error) {
	slog.Error(
		"API request failed",
		"status", status,
		"request_id", response.Header().Get(requestIDHeader),
		"error", err,
	)
}

func writeQueryError(response http.ResponseWriter, err error) bool {
	var queryError *topologyapp.QueryError
	if !errors.As(err, &queryError) {
		return false
	}
	writeAPIError(response, http.StatusBadRequest, APIError{Code: queryError.Code, Message: queryError.Message, Details: queryError.Details})
	return true
}

func writeAPIError(response http.ResponseWriter, status int, value APIError) {
	requestID := response.Header().Get(requestIDHeader)
	if value.RequestID != "" && value.RequestID != requestID {
		details := make(map[string]any, len(value.Details)+1)
		for key, detail := range value.Details {
			details[key] = detail
		}
		if _, exists := details["provider_request_id"]; !exists {
			details["provider_request_id"] = value.RequestID
		}
		value.Details = details
	}
	value.RequestID = requestID
	writeJSON(response, status, errorBody{Error: value})
}
