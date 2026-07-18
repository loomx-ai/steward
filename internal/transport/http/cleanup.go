package httptransport

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func (a *API) createCleanupTask(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Selectors      []plan.CleanupSelector           `json:"selectors"`
		RequestOptions map[asset.AssetID]map[string]any `json:"request_options"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	aggregate, err := a.dependencies.CleanupTasks.CreateTask(request.Context(), cleanup.CreateTaskRequest{ConnectionID: selectedConnectionID(request), Selectors: input.Selectors, RequestOptions: input.RequestOptions, CreatedBy: principal.Subject})
	if err != nil {
		if errors.Is(err, cleanup.ErrInventoryReconciliationPending) {
			writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.inventory_reconciling", Message: err.Error()})
			return
		}
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, aggregate)
}

func (a *API) getCleanupTask(response http.ResponseWriter, request *http.Request) {
	aggregate, err := a.dependencies.CleanupTasks.GetTask(
		request.Context(),
		plan.CleanupTaskID(chi.URLParam(request, "id")),
		selectedConnectionID(request),
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, aggregate)
}

func (a *API) addCleanupTaskAssets(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Selectors []plan.CleanupSelector `json:"selectors"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if len(input.Selectors) == 0 {
		writeError(response, http.StatusBadRequest, errors.New("at least one asset selector is required"))
		return
	}
	assetIDs := make([]asset.AssetID, 0, len(input.Selectors))
	for _, selector := range input.Selectors {
		if selector.Kind != plan.SelectorAsset || selector.AssetID == "" {
			writeError(response, http.StatusBadRequest, errors.New("only asset selectors can be added to a cleanup task"))
			return
		}
		assetIDs = append(assetIDs, selector.AssetID)
	}
	principal, _ := principalFromContext(request.Context())
	aggregate, err := a.dependencies.CleanupTasks.AddTaskAssets(request.Context(), cleanup.AddTaskAssetsRequest{
		ConnectionID: selectedConnectionID(request), CleanupTaskID: plan.CleanupTaskID(chi.URLParam(request, "id")), AssetIDs: assetIDs, UpdatedBy: principal.Subject,
	})
	if err != nil {
		switch {
		case errors.Is(err, cleanup.ErrTaskNotEditable):
			writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.not_editable", Message: err.Error()})
		case errors.Is(err, cleanup.ErrInventoryReconciliationPending):
			writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.inventory_reconciling", Message: err.Error()})
		default:
			repositoryError(response, err)
		}
		return
	}
	writeJSON(response, http.StatusOK, aggregate)
}

func (a *API) listCleanupTasks(response http.ResponseWriter, request *http.Request) {
	page, err := a.dependencies.Repositories.CleanupTasks().ListTasks(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}
