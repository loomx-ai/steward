package httptransport

import "net/http"

func (a *API) listAudits(response http.ResponseWriter, request *http.Request) {
	page, err := a.dependencies.Repositories.Audits().ListAuditEvents(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}
