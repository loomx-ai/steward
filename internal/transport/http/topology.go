package httptransport

import (
	"fmt"
	"net/http"
	"strconv"

	topologyapp "github.com/loomx-ai/steward/internal/app/topology"
	"github.com/loomx-ai/steward/internal/core/asset"
)

func (a *API) topology(response http.ResponseWriter, request *http.Request) {
	if a.dependencies.Topology == nil {
		writeError(response, http.StatusServiceUnavailable, fmt.Errorf("topology service is unavailable"))
		return
	}
	for _, legacy := range []string{"parent_key", "depth", "node_limit"} {
		if request.URL.Query().Has(legacy) {
			writeAPIError(response, http.StatusBadRequest, APIError{
				Code: "topology.query_invalid", Message: fmt.Sprintf("%s is not supported", legacy),
			})
			return
		}
	}
	limit, err := optionalInteger(request, "limit")
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "topology.limit_invalid", Message: err.Error()})
		return
	}
	result, err := a.dependencies.Topology.Query(request.Context(), topologyapp.Query{
		ConnectionID:    asset.ConnectionID(request.URL.Query().Get("connection_id")),
		FocusKey:        request.URL.Query().Get("focus_key"),
		Cursor:          request.URL.Query().Get("cursor"),
		Limit:           limit,
		ResourceClass:   request.URL.Query().Get("resource_class"),
		ResourceKindIDs: resourceKindIDs(request.URL.Query()["resource_kind_id"]),
		Risk:            request.URL.Query().Get("risk"),
		ResourceQuery:   request.URL.Query().Get("resource_query"),
	})
	if err != nil {
		if !writeQueryError(response, err) {
			repositoryError(response, err)
		}
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func optionalInteger(request *http.Request, name string) (int, error) {
	value := request.URL.Query().Get(name)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}
