package httptransport

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *API) startAliCloudOAuthFlow(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.OAuthFlows == nil {
		writeAPIError(response, http.StatusServiceUnavailable, APIError{
			Code: "oauth_flow_unavailable", Message: "Alibaba Cloud OAuth flow service is unavailable",
		})
		return
	}
	var input struct {
		Site asset.ConnectionSite `json:"site"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{
			Code: "invalid_connection_site", Message: "Alibaba Cloud site is required",
		})
		return
	}
	principal, _ := principalFromContext(request.Context())
	view, err := a.dependencies.OAuthFlows.Start(request.Context(), principal.Subject, input.Site)
	if err != nil {
		writeOAuthFlowError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, view)
}

func (a *API) getAliCloudOAuthFlow(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.OAuthFlows == nil {
		writeAPIError(response, http.StatusServiceUnavailable, APIError{
			Code: "oauth_flow_unavailable", Message: "Alibaba Cloud OAuth flow service is unavailable",
		})
		return
	}
	principal, _ := principalFromContext(request.Context())
	view, err := a.dependencies.OAuthFlows.Get(
		request.Context(),
		principal.Subject,
		strings.TrimSpace(chi.URLParam(request, "id")),
	)
	if err != nil {
		writeOAuthFlowError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func writeOAuthFlowError(response http.ResponseWriter, err error) {
	var flowError *contracts.OAuthFlowError
	if !errors.As(err, &flowError) {
		writeConnectionError(response, err)
		return
	}
	status := http.StatusConflict
	switch flowError.Code {
	case "invalid_connection_site", "oauth_subject_required":
		status = http.StatusBadRequest
	case "oauth_flow_not_found":
		status = http.StatusNotFound
	case "oauth_flow_expired":
		status = http.StatusGone
	case "oauth_flow_failed":
		status = http.StatusUnprocessableEntity
	case "oauth_flow_unavailable", "oauth_loopback_unavailable", "oauth_loopback_failed":
		status = http.StatusServiceUnavailable
	}
	writeAPIError(response, status, APIError{Code: flowError.Code, Message: flowError.Error()})
}
