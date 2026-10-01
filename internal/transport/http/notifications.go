package httptransport

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/app/notification"
)

func (a *API) notifications(response http.ResponseWriter) (*notification.Service, bool) {
	if a.dependencies.Notifications == nil {
		writeError(response, http.StatusInternalServerError, errors.New("notifications are unavailable"))
		return nil, false
	}
	return a.dependencies.Notifications, true
}

func writeNotificationError(response http.ResponseWriter, err error) {
	var invalid *notification.InvalidError
	if errors.As(err, &invalid) {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: invalid.Code, Message: invalid.Message})
		return
	}
	repositoryError(response, err)
}

func (a *API) listNotificationChannels(response http.ResponseWriter, request *http.Request) {
	service, ok := a.notifications(response)
	if !ok {
		return
	}
	views, err := service.List(request.Context())
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, views)
}

func (a *API) createNotificationChannel(response http.ResponseWriter, request *http.Request) {
	service, ok := a.notifications(response)
	if !ok {
		return
	}
	var input notification.Input
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	view, err := service.Create(request.Context(), input, principal.Subject)
	if err != nil {
		writeNotificationError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, view)
}

func (a *API) updateNotificationChannel(response http.ResponseWriter, request *http.Request) {
	service, ok := a.notifications(response)
	if !ok {
		return
	}
	var input notification.Input
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	view, err := service.Update(request.Context(), chi.URLParam(request, "id"), input, principal.Subject)
	if err != nil {
		writeNotificationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (a *API) deleteNotificationChannel(response http.ResponseWriter, request *http.Request) {
	service, ok := a.notifications(response)
	if !ok {
		return
	}
	principal, _ := principalFromContext(request.Context())
	if err := service.Delete(request.Context(), chi.URLParam(request, "id"), principal.Subject); err != nil {
		writeNotificationError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *API) testNotificationChannel(response http.ResponseWriter, request *http.Request) {
	service, ok := a.notifications(response)
	if !ok {
		return
	}
	view, err := service.Test(request.Context(), chi.URLParam(request, "id"))
	if err != nil {
		writeNotificationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}
