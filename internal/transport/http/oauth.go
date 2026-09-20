package httptransport

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// oauthFlows resolves the browser authorization service for the provider named
// in the route. A provider without one either has no OAuth credential type or
// runs in a deployment that cannot receive a loopback callback.
func (a *API) oauthFlows(request *http.Request) (contracts.OAuthFlowService, asset.Provider, bool) {
	provider := asset.Provider(strings.TrimSpace(chi.URLParam(request, "provider")))
	flows, ok := a.dependencies.OAuthFlows[provider]
	if !ok || flows == nil {
		return nil, provider, false
	}
	return flows, provider, true
}

func (a *API) startOAuthFlow(response http.ResponseWriter, request *http.Request) {
	flows, _, ok := a.oauthFlows(request)
	if !ok {
		writeOAuthFlowUnavailable(response)
		return
	}
	var input struct {
		Params map[string]string `json:"params"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{
			Code: "invalid_oauth_parameters", Message: "OAuth flow parameters are invalid",
		})
		return
	}
	principal, _ := principalFromContext(request.Context())
	view, err := flows.Start(request.Context(), principal.Subject, input.Params)
	if err != nil {
		writeOAuthFlowError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, view)
}

func (a *API) getOAuthFlow(response http.ResponseWriter, request *http.Request) {
	flows, _, ok := a.oauthFlows(request)
	if !ok {
		writeOAuthFlowUnavailable(response)
		return
	}
	principal, _ := principalFromContext(request.Context())
	view, err := flows.Get(
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

func (a *API) listOAuthFlowTargets(response http.ResponseWriter, request *http.Request) {
	flows, _, ok := a.oauthFlows(request)
	if !ok {
		writeOAuthFlowUnavailable(response)
		return
	}
	principal, _ := principalFromContext(request.Context())
	targets, err := flows.Targets(
		request.Context(),
		principal.Subject,
		strings.TrimSpace(chi.URLParam(request, "id")),
	)
	if err != nil {
		writeOAuthFlowError(response, err)
		return
	}
	if targets == nil {
		targets = []contracts.OAuthTarget{}
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, targets)
}

func writeOAuthFlowUnavailable(response http.ResponseWriter) {
	writeAPIError(response, http.StatusServiceUnavailable, APIError{
		Code: "oauth_flow_unavailable", Message: "Browser authorization is unavailable for this cloud",
	})
}

func writeOAuthFlowError(response http.ResponseWriter, err error) {
	var flowError *contracts.OAuthFlowError
	if !errors.As(err, &flowError) {
		writeConnectionError(response, err)
		return
	}
	status := http.StatusConflict
	switch flowError.Code {
	case "invalid_connection_site", "oauth_subject_required", "invalid_oauth_parameters":
		status = http.StatusBadRequest
	case "oauth_flow_not_found":
		status = http.StatusNotFound
	case "oauth_flow_expired":
		status = http.StatusGone
	case "oauth_flow_failed":
		status = http.StatusUnprocessableEntity
	case "oauth_target_invalid", "oauth_target_mismatch":
		status = http.StatusUnprocessableEntity
	case "oauth_flow_unavailable", "oauth_loopback_unavailable", "oauth_loopback_failed", "oauth_targets_unavailable":
		status = http.StatusServiceUnavailable
	}
	writeAPIError(response, status, APIError{Code: flowError.Code, Message: flowError.Error()})
}
