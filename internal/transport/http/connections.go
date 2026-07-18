package httptransport

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	credentialstore "github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type credentialInput struct {
	Type      asset.CredentialType `json:"type"`
	Values    map[string]string    `json:"values"`
	ExpiresAt *time.Time           `json:"expires_at"`
}

func (input credentialInput) contract() contracts.Credential {
	values := make(map[string]string, len(input.Values))
	for key, value := range input.Values {
		values[key] = strings.TrimSpace(value)
	}
	delete(values, "expires_at")
	return contracts.Credential{Type: input.Type, Values: values, ExpiresAt: input.ExpiresAt}
}

func (a *API) listConnections(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Connections == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("connection service is unavailable"))
		return
	}
	page, err := a.dependencies.Connections.List(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func (a *API) createConnection(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Connections == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("connection service is unavailable"))
		return
	}
	var input struct {
		Name       string               `json:"name"`
		Provider   asset.Provider       `json:"provider"`
		Site       asset.ConnectionSite `json:"site"`
		Credential credentialInput      `json:"credential"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: err.Error()})
		return
	}
	principal, _ := principalFromContext(request.Context())
	if input.Credential.Type == asset.CredentialAliCloudOAuth {
		flowID, err := oauthFlowID(input.Provider, input.Credential)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: err.Error()})
			return
		}
		if a.dependencies.OAuthFlows == nil {
			writeOAuthFlowError(response, &contracts.OAuthFlowError{Code: "oauth_flow_unavailable", Message: "Alibaba Cloud OAuth flow service is unavailable"})
			return
		}
		var value connectionapp.View
		err = a.dependencies.OAuthFlows.Consume(request.Context(), principal.Subject, flowID, input.Site, func(credential contracts.Credential) error {
			var createErr error
			value, createErr = a.dependencies.Connections.Create(request.Context(), connectionapp.CreateRequest{
				Name: input.Name, Provider: input.Provider, Site: input.Site, Credential: credential, Actor: principal.Subject,
			})
			return createErr
		})
		if err != nil {
			writeOAuthFlowError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, value)
		return
	}
	if _, exists := input.Credential.Values["flow_id"]; exists {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: "OAuth flow ID is only valid for an OAuth credential"})
		return
	}
	value, err := a.dependencies.Connections.Create(request.Context(), connectionapp.CreateRequest{
		Name: input.Name, Provider: input.Provider, Site: input.Site, Credential: input.Credential.contract(), Actor: principal.Subject,
	})
	if err != nil {
		writeConnectionError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, value)
}

func (a *API) renameConnection(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Connections == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("connection service is unavailable"))
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	value, err := a.dependencies.Connections.Rename(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")), input.Name, principal.Subject)
	if err != nil {
		writeConnectionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (a *API) replaceConnectionCredential(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Connections == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("connection service is unavailable"))
		return
	}
	var input credentialInput
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: err.Error()})
		return
	}
	principal, _ := principalFromContext(request.Context())
	if input.Type == asset.CredentialAliCloudOAuth {
		connectionID := asset.ConnectionID(chi.URLParam(request, "id"))
		connection, err := a.dependencies.Repositories.Connections().GetConnection(request.Context(), connectionID)
		if err != nil || connection.Status == asset.ConnectionDeleted {
			if err == nil {
				err = persistence.ErrNotFound
			}
			writeConnectionError(response, err)
			return
		}
		flowID, err := oauthFlowID(connection.Provider, input)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: err.Error()})
			return
		}
		if a.dependencies.OAuthFlows == nil {
			writeOAuthFlowError(response, &contracts.OAuthFlowError{Code: "oauth_flow_unavailable", Message: "Alibaba Cloud OAuth flow service is unavailable"})
			return
		}
		var value connectionapp.View
		err = a.dependencies.OAuthFlows.Consume(request.Context(), principal.Subject, flowID, connection.Site, func(credential contracts.Credential) error {
			var replaceErr error
			value, replaceErr = a.dependencies.Connections.ReplaceCredential(request.Context(), connectionID, credential, principal.Subject)
			return replaceErr
		})
		if err != nil {
			writeOAuthFlowError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, value)
		return
	}
	if _, exists := input.Values["flow_id"]; exists {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: "OAuth flow ID is only valid for an OAuth credential"})
		return
	}
	value, err := a.dependencies.Connections.ReplaceCredential(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")), input.contract(), principal.Subject)
	if err != nil {
		writeConnectionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func oauthFlowID(provider asset.Provider, input credentialInput) (string, error) {
	if provider != asset.ProviderAliCloud {
		return "", errors.New("OAuth credentials are only supported for Alibaba Cloud connections")
	}
	if input.ExpiresAt != nil || len(input.Values) != 1 {
		return "", errors.New("OAuth credential input must contain only a flow_id")
	}
	flowID := strings.TrimSpace(input.Values["flow_id"])
	if flowID == "" {
		return "", errors.New("OAuth credential flow_id is required")
	}
	return flowID, nil
}

func (a *API) validateConnection(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Connections == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("connection service is unavailable"))
		return
	}
	principal, _ := principalFromContext(request.Context())
	value, err := a.dependencies.Connections.Validate(
		request.Context(),
		asset.ConnectionID(chi.URLParam(request, "id")),
		principal.Subject,
	)
	if err != nil {
		writeConnectionValidationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (a *API) deleteConnection(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Connections == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("connection service is unavailable"))
		return
	}
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	if err := a.dependencies.Connections.Delete(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")), input.Confirmation, principal.Subject); err != nil {
		writeConnectionError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func writeConnectionValidationError(response http.ResponseWriter, err error) {
	var providerErr *contracts.ProviderCallError
	if errors.As(err, &providerErr) {
		writeProviderConnectionError(response, providerErr.Provider)
		return
	}
	writeConnectionError(response, err)
}

func writeConnectionError(response http.ResponseWriter, err error) {
	var providerErr *contracts.ProviderCallError
	var credentialErr *contracts.CredentialValidationError
	if errors.As(err, &providerErr) {
		writeProviderConnectionError(response, contracts.SanitizeProviderError(providerErr.Provider))
		return
	}
	if errors.As(err, &credentialErr) {
		writeAPIError(response, http.StatusUnprocessableEntity, APIError{
			Code: credentialErr.Code, Message: credentialErr.Message,
		})
		return
	}
	switch {
	case errors.Is(err, connectionapp.ErrIdentityMismatch):
		writeAPIError(response, http.StatusConflict, APIError{Code: "credential_identity_mismatch", Message: err.Error()})
	case errors.Is(err, connectionapp.ErrConnectionBusy):
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_busy", Message: err.Error()})
	case errors.Is(err, connectionapp.ErrValidationStale):
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_validation_stale", Message: "the connection credential changed during validation; validate again"})
	case errors.Is(err, connectionapp.ErrConnectionChanged):
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_changed", Message: "the connection changed during this operation; try again"})
	case errors.Is(err, connectionapp.ErrConfirmationMismatch):
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_confirmation_mismatch", Message: err.Error()})
	case errors.Is(err, connectionapp.ErrInvalidSite):
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_connection_site", Message: err.Error()})
	case errors.Is(err, connectionapp.ErrInvalidInput):
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "invalid_credential_fields", Message: err.Error()})
	case errors.Is(err, credentialstore.ErrUnavailable):
		writeAPIError(response, http.StatusUnprocessableEntity, APIError{Code: "credential_unavailable", Message: "the stored cloud credential is unavailable"})
	case errors.Is(err, persistence.ErrNotFound):
		writeError(response, http.StatusNotFound, err)
	default:
		writeError(response, http.StatusInternalServerError, errors.New("connection operation failed"))
	}
}

func writeProviderConnectionError(response http.ResponseWriter, providerError execution.ProviderError) {
	writeAPIError(response, http.StatusUnprocessableEntity, APIError{
		Code: "credential_validation_failed", Message: "cloud provider credential validation failed",
		Details: map[string]any{
			"category":            providerError.Category,
			"provider_code":       providerError.Code,
			"provider_message":    providerError.Message,
			"provider_request_id": providerError.RequestID,
		},
		RequestID: providerError.RequestID,
	})
}
