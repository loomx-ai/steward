package httptransport

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	regionapp "github.com/loomx-ai/steward/internal/app/region"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

type regionView struct {
	asset.ConnectionRegion
	Name string `json:"name"`
}

func newRegionView(region asset.ConnectionRegion) regionView {
	return regionView{ConnectionRegion: region, Name: region.EffectiveName()}
}

func (a *API) listConnectionRegions(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Regions == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("region service is unavailable"))
		return
	}
	lifecycle := asset.RegionLifecycle(strings.TrimSpace(request.URL.Query().Get("lifecycle")))
	if lifecycle != "" && lifecycle != asset.RegionActive && lifecycle != asset.RegionRetired && lifecycle != asset.RegionExcluded {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.lifecycle_invalid", Message: "region lifecycle must be active, retired, or excluded"})
		return
	}
	page, err := a.dependencies.Regions.List(request.Context(), persistence.RegionListOptions{
		ConnectionID: asset.ConnectionID(chi.URLParam(request, "id")), Lifecycle: lifecycle,
		Query: request.URL.Query().Get("q"),
	})
	if err != nil {
		writeRegionError(response, err)
		return
	}
	result := persistence.Page[regionView]{Items: make([]regionView, 0, len(page.Items))}
	for _, region := range page.Items {
		result.Items = append(result.Items, newRegionView(region))
	}
	writeJSON(response, http.StatusOK, result)
}

func (a *API) addConnectionRegion(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Regions == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("region service is unavailable"))
		return
	}
	var input struct {
		RegionID string `json:"region_id"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.request_invalid", Message: err.Error()})
		return
	}
	principal, _ := principalFromContext(request.Context())
	region, err := a.dependencies.Regions.Add(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")), input.RegionID, input.Name, principal.Subject)
	if err != nil {
		writeRegionError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, newRegionView(region))
}

func (a *API) refreshConnectionRegions(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.RegionRefreshes == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("region refresh queue is unavailable"))
		return
	}
	job, err := a.dependencies.RegionRefreshes.Enqueue(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")))
	if err != nil {
		writeRegionError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status})
}

func (a *API) patchConnectionRegion(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Regions == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("region service is unavailable"))
		return
	}
	var input struct {
		Name      *string                `json:"name"`
		ResetName bool                   `json:"reset_name"`
		Lifecycle *asset.RegionLifecycle `json:"lifecycle"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.request_invalid", Message: err.Error()})
		return
	}
	mutations := 0
	if input.Name != nil {
		mutations++
	}
	if input.ResetName {
		mutations++
	}
	if input.Lifecycle != nil {
		mutations++
	}
	if mutations != 1 {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.patch_ambiguous", Message: "exactly one region mutation is required"})
		return
	}
	connectionID := asset.ConnectionID(chi.URLParam(request, "id"))
	regionID := chi.URLParam(request, "region_id")
	principal, _ := principalFromContext(request.Context())
	var (
		region asset.ConnectionRegion
		err    error
	)
	switch {
	case input.Name != nil:
		region, err = a.dependencies.Regions.Rename(request.Context(), connectionID, regionID, *input.Name, principal.Subject)
	case input.ResetName:
		region, err = a.dependencies.Regions.ResetName(request.Context(), connectionID, regionID, principal.Subject)
	case *input.Lifecycle == asset.RegionActive:
		region, err = a.dependencies.Regions.Activate(request.Context(), connectionID, regionID, principal.Subject)
	case *input.Lifecycle == asset.RegionRetired:
		region, err = a.dependencies.Regions.Retire(request.Context(), connectionID, regionID, principal.Subject)
	default:
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.lifecycle_invalid", Message: "PATCH lifecycle must be active or retired"})
		return
	}
	if err != nil {
		writeRegionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, newRegionView(region))
}

func (a *API) excludeConnectionRegion(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Regions == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("region service is unavailable"))
		return
	}
	principal, _ := principalFromContext(request.Context())
	region, err := a.dependencies.Regions.Exclude(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")), chi.URLParam(request, "region_id"), principal.Subject)
	if err != nil {
		writeRegionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, newRegionView(region))
}

func (a *API) restoreConnectionRegion(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Regions == nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("region service is unavailable"))
		return
	}
	principal, _ := principalFromContext(request.Context())
	region, err := a.dependencies.Regions.Restore(request.Context(), asset.ConnectionID(chi.URLParam(request, "id")), chi.URLParam(request, "region_id"), principal.Subject)
	if err != nil {
		writeRegionError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, newRegionView(region))
}

func writeRegionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, asset.ErrConnectionNotValidated):
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_not_validated", Message: err.Error()})
	case errors.Is(err, regionapp.ErrRegionNotFound), errors.Is(err, persistence.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, APIError{Code: "region.not_found", Message: "the connection region was not found"})
	case errors.Is(err, regionapp.ErrRegionConflict), errors.Is(err, persistence.ErrConflict):
		writeAPIError(response, http.StatusConflict, APIError{Code: "region.conflict", Message: err.Error()})
	case errors.Is(err, regionapp.ErrRegionInactive):
		writeAPIError(response, http.StatusConflict, APIError{Code: "region.lifecycle_conflict", Message: err.Error()})
	case errors.Is(err, regionapp.ErrRegionIDEmpty):
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.id_required", Message: err.Error()})
	case errors.Is(err, regionapp.ErrRegionNameEmpty):
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.name_required", Message: err.Error()})
	default:
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "region.request_invalid", Message: err.Error()})
	}
}
