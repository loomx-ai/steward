package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/app/inventory"
	regionapp "github.com/loomx-ai/steward/internal/app/region"
	topologyapp "github.com/loomx-ai/steward/internal/app/topology"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
	"github.com/loomx-ai/steward/internal/transport/events"
)

type BundleCatalog interface {
	Bundles() []spec.Bundle
}

type ResourceKindCatalog interface {
	ResourceKinds(asset.Provider) ([]asset.ResourceKind, string, bool)
}

type ProviderDirectory interface {
	ProviderDescriptors() []contracts.ProviderDescriptor
}

type NetworkTargetDirectory interface {
	ResolveNetworkTargetDiscoverer(asset.Provider) (contracts.NetworkTargetDiscoverer, error)
}

type Dependencies struct {
	Repositories    persistence.Repositories
	CleanupTasks    *cleanup.Service
	Connections     *connectionapp.Service
	Regions         *regionapp.Service
	Scans           *inventory.Creator
	ScanControls    *inventory.ControlService
	NetworkTargets  NetworkTargetDirectory
	RegionRefreshes connectionapp.RegionRefreshQueue
	Topology        *topologyapp.Service
	Bundles         BundleCatalog
	Providers       ProviderDirectory
	OAuthFlows      contracts.OAuthFlowService
	Authenticator   Authenticator
	AuthMode        string
	SSEPollInterval time.Duration
}

type API struct {
	dependencies Dependencies
}

type selectedConnectionContextKey struct{}

func NewRouter(dependencies Dependencies) http.Handler {
	api := &API{dependencies: dependencies}
	router := chi.NewRouter()
	router.Use(withRequestID)
	router.Get("/api/session", api.session)
	router.Route("/api", func(router chi.Router) {
		router.Use(authenticate(dependencies.Authenticator))
		router.Get("/providers", requireRole(RoleViewer, api.listProviders))
		router.Get("/providers/catalog", requireRole(RoleViewer, api.listCatalog))
		router.Post("/providers/alicloud/oauth/flows", requireRole(RoleAdmin, api.startAliCloudOAuthFlow))
		router.Get("/providers/alicloud/oauth/flows/{id}", requireRole(RoleAdmin, api.getAliCloudOAuthFlow))
		router.Get("/connections", requireRole(RoleViewer, api.listConnections))
		router.Post("/connections", requireRole(RoleAdmin, api.createConnection))
		router.Patch("/connections/{id}", requireRole(RoleAdmin, api.renameConnection))
		router.Put("/connections/{id}/credential", requireRole(RoleAdmin, api.replaceConnectionCredential))
		router.Post("/connections/{id}/validate", requireRole(RoleAdmin, api.validateConnection))
		router.Delete("/connections/{id}", requireRole(RoleAdmin, api.deleteConnection))
		router.Get("/connections/{id}/regions", requireRole(RoleViewer, api.listConnectionRegions))
		router.Post("/connections/{id}/regions", requireRole(RoleAdmin, api.addConnectionRegion))
		router.Post("/connections/{id}/regions/refresh", requireRole(RoleAdmin, api.refreshConnectionRegions))
		router.Patch("/connections/{id}/regions/{region_id}", requireRole(RoleAdmin, api.patchConnectionRegion))
		router.Delete("/connections/{id}/regions/{region_id}", requireRole(RoleAdmin, api.excludeConnectionRegion))
		router.Post("/connections/{id}/regions/{region_id}/restore", requireRole(RoleAdmin, api.restoreConnectionRegion))
		router.Group(func(router chi.Router) {
			router.Use(api.requireConnectionContext)
			router.Get("/scopes", requireRole(RoleViewer, api.listScopes))
			router.Get("/scans", requireRole(RoleViewer, api.listScans))
			router.Post("/scans", requireRole(RoleOperator, api.createScan))
			router.Get("/scans/{id}", requireRole(RoleViewer, api.getScan))
			router.Post("/scans/{id}/pause", requireRole(RoleOperator, api.pauseScan))
			router.Post("/scans/{id}/resume", requireRole(RoleOperator, api.resumeScan))
			router.Post("/scans/{id}/cancel", requireRole(RoleOperator, api.cancelScan))
			router.Post("/scans/{id}/retry", requireRole(RoleOperator, api.retryScan))
			router.Get("/scans/{id}/logs", requireRole(RoleViewer, api.scanLogs))
			router.Get("/scans/{id}/events", requireRole(RoleViewer, api.scanEvents))
			router.Get("/scan-targets/vpcs", requireRole(RoleViewer, api.listVPCTargets))
			router.Get("/scan-targets/vswitches", requireRole(RoleViewer, api.listVSwitchTargets))
			router.Get("/assets", requireRole(RoleViewer, api.listAssets))
			router.Patch("/assets/{id}", requireRole(RoleOperator, api.patchAsset))
			router.Get("/topology", requireRole(RoleViewer, api.topology))
			router.Get("/assets/{id}/graph", requireRole(RoleViewer, api.assetGraph))
			router.Get("/assets/{id}/lifecycle", requireRole(RoleViewer, api.assetLifecycle))
			router.Get("/findings", requireRole(RoleViewer, api.listFindings))
			router.Get("/cleanup", requireRole(RoleViewer, api.listCleanupTasks))
			router.Post("/cleanup", requireRole(RoleOperator, api.createCleanupTask))
			router.Get("/cleanup/{id}", requireRole(RoleViewer, api.getCleanupTask))
			router.Post("/cleanup/{id}/assets", requireRole(RoleOperator, api.addCleanupTaskAssets))
			router.Get("/cleanup/{id}/logs", requireRole(RoleViewer, api.cleanupTaskLogs))
			router.Get("/cleanup/{id}/events", requireRole(RoleViewer, api.cleanupTaskEvents))
			router.Get("/cleanup/{id}/executions", requireRole(RoleViewer, api.listCleanupTaskExecutions))
			router.Post("/cleanup/{id}/executions", requireRole(RoleOperator, api.createExecution))
			router.Post("/cleanup/{id}/continue", requireRole(RoleOperator, api.continueExecution))
			router.Post("/cleanup/{id}/pause", requireRole(RoleOperator, api.pauseExecution))
			router.Post("/cleanup/{id}/resume", requireRole(RoleOperator, api.resumeExecution))
			router.Get("/execution-attempts", requireRole(RoleViewer, api.listExecutions))
			router.Get("/execution-attempts/{id}", requireRole(RoleViewer, api.getExecution))
			router.Get("/execution-attempts/{id}/actions", requireRole(RoleViewer, api.listExecutionActions))
			router.Get("/jobs/{id}", requireRole(RoleViewer, api.getJob))
			router.Get("/jobs/{id}/events", requireRole(RoleViewer, api.jobEvents))
			router.Get("/audit-events", requireRole(RoleViewer, api.listAudits))
		})
	})
	return router
}

func (a *API) session(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	mode := a.dependencies.AuthMode
	if mode == "" {
		mode = "token"
	}
	var principal *Principal
	if a.dependencies.Authenticator != nil {
		if authenticated, err := a.dependencies.Authenticator.Authenticate(request); err == nil {
			principal = &authenticated
		}
	}
	if mode == "local" && principal == nil {
		writeError(response, http.StatusForbidden, ErrForbidden)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Mode          string     `json:"mode"`
		Authenticated bool       `json:"authenticated"`
		Principal     *Principal `json:"principal"`
	}{Mode: mode, Authenticated: principal != nil, Principal: principal})
}

func (a *API) listProviders(response http.ResponseWriter, _ *http.Request) {
	if a.dependencies.Providers == nil {
		writeJSON(response, http.StatusOK, []contracts.ProviderDescriptor{})
		return
	}
	writeJSON(response, http.StatusOK, a.dependencies.Providers.ProviderDescriptors())
}

func (a *API) listCatalog(response http.ResponseWriter, _ *http.Request) {
	if a.dependencies.Bundles == nil {
		writeJSON(response, http.StatusOK, []providerCatalogBundle{})
		return
	}
	bundles := a.dependencies.Bundles.Bundles()
	catalog, hasRuntimeKinds := a.dependencies.Bundles.(ResourceKindCatalog)
	result := make([]providerCatalogBundle, 0, len(bundles))
	for _, bundle := range bundles {
		specs := append([]spec.CompiledSpec{}, bundle.Specs...)
		kinds := compiledResourceKinds(specs)
		kindsRevision := bundle.Revision
		if hasRuntimeKinds {
			if runtimeKinds, revision, ok := catalog.ResourceKinds(bundle.Provider); ok {
				kinds = append([]asset.ResourceKind{}, runtimeKinds...)
				kindsRevision = revision
				runtimeByID := make(map[asset.ResourceKindID]asset.ResourceKind, len(runtimeKinds))
				for _, kind := range runtimeKinds {
					runtimeByID[kind.ID] = kind
				}
				for specIndex := range specs {
					if kind, exists := runtimeByID[specs[specIndex].ResourceKind.ID]; exists {
						specs[specIndex].ResourceKind = kind
					}
				}
			}
		}
		result = append(result, providerCatalogBundle{
			Provider:      bundle.Provider,
			Specs:         specs,
			Revision:      bundle.Revision,
			Hash:          bundle.Hash,
			Kinds:         kinds,
			KindsRevision: kindsRevision,
		})
	}
	writeJSON(response, http.StatusOK, result)
}

type providerCatalogBundle struct {
	Provider      asset.Provider       `json:"provider"`
	Specs         []spec.CompiledSpec  `json:"specs"`
	Revision      string               `json:"revision"`
	Hash          string               `json:"hash"`
	Kinds         []asset.ResourceKind `json:"kinds"`
	KindsRevision string               `json:"kinds_revision"`
}

func compiledResourceKinds(specs []spec.CompiledSpec) []asset.ResourceKind {
	kinds := make([]asset.ResourceKind, 0, len(specs))
	for _, compiled := range specs {
		kinds = append(kinds, compiled.ResourceKind)
	}
	return kinds
}

func (a *API) getJob(response http.ResponseWriter, request *http.Request) {
	job, err := a.selectedJob(request)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, job)
}

func (a *API) jobEvents(response http.ResponseWriter, request *http.Request) {
	request.SetPathValue("id", chi.URLParam(request, "id"))
	if _, err := a.selectedJob(request); err != nil {
		repositoryError(response, err)
		return
	}
	events.NewJobEventHandler(a.dependencies.Repositories.Jobs(), a.dependencies.SSEPollInterval).ServeHTTP(response, request)
}

func (a *API) selectedJob(request *http.Request) (execution.Job, error) {
	job, err := a.dependencies.Repositories.Jobs().GetJob(request.Context(), execution.JobID(chi.URLParam(request, "id")))
	if err == nil && job.ConnectionID != selectedConnectionID(request) {
		err = persistence.ErrNotFound
	}
	return job, err
}

func pageOptions(request *http.Request) persistence.ListOptions {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	return persistence.ListOptions{
		Limit: limit, Cursor: request.URL.Query().Get("cursor"),
		ConnectionID:  asset.ConnectionID(request.URL.Query().Get("connection_id")),
		CleanupTaskID: strings.TrimSpace(request.URL.Query().Get("cleanup_task_id")),
		Provider:      strings.TrimSpace(request.URL.Query().Get("provider")),
		Query:         strings.TrimSpace(request.URL.Query().Get("q")),
		Capability:    strings.TrimSpace(request.URL.Query().Get("capability")),
		ResourceKindIDs: resourceKindIDs(
			request.URL.Query()["resource_kind_id"],
		),
		AssetIDs:      assetIDs(request.URL.Query()["asset_id"]),
		NativeIDs:     uniqueStrings(request.URL.Query()["native_id"]),
		AssetCanvas:   persistence.AssetCanvas(strings.TrimSpace(request.URL.Query().Get("canvas"))),
		RegionID:      strings.TrimSpace(request.URL.Query().Get("region_id")),
		VPCID:         strings.TrimSpace(request.URL.Query().Get("vpc_id")),
		SearchOrder:   strings.TrimSpace(request.URL.Query().Get("order")) == "panorama-search",
		IncludeClosed: strings.TrimSpace(request.URL.Query().Get("include_closed")) == "true",
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func resourceKindIDs(values []string) []asset.ResourceKindID {
	seen := make(map[asset.ResourceKindID]struct{}, len(values))
	result := make([]asset.ResourceKindID, 0, len(values))
	for _, value := range values {
		id := asset.ResourceKindID(strings.TrimSpace(value))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func assetIDs(values []string) []asset.AssetID {
	seen := make(map[asset.AssetID]struct{}, len(values))
	result := make([]asset.AssetID, 0, len(values))
	for _, value := range values {
		id := asset.AssetID(strings.TrimSpace(value))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func (a *API) requireConnectionContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		id := asset.ConnectionID(strings.TrimSpace(request.URL.Query().Get("connection_id")))
		if id == "" {
			writeAPIError(response, http.StatusBadRequest, APIError{Code: "connection_required", Message: "a cloud connection must be selected"})
			return
		}
		connection, err := a.dependencies.Repositories.Connections().GetConnection(request.Context(), id)
		if errors.Is(err, persistence.ErrNotFound) || connection.Status == asset.ConnectionDeleted {
			writeAPIError(response, http.StatusNotFound, APIError{Code: "connection_not_found", Message: "the selected cloud connection was not found"})
			return
		}
		if err != nil {
			repositoryError(response, err)
			return
		}
		if connection.Status != asset.ConnectionActive {
			writeAPIError(response, http.StatusConflict, APIError{Code: "connection_not_validated", Message: "the selected cloud connection has not passed validation"})
			return
		}
		next.ServeHTTP(response, request.WithContext(
			context.WithValue(request.Context(), selectedConnectionContextKey{}, connection),
		))
	})
}

func selectedConnectionID(request *http.Request) asset.ConnectionID {
	return asset.ConnectionID(strings.TrimSpace(request.URL.Query().Get("connection_id")))
}

func selectedConnection(request *http.Request) (asset.CloudConnection, bool) {
	connection, ok := request.Context().Value(selectedConnectionContextKey{}).(asset.CloudConnection)
	return connection, ok
}

func decodeJSON(request *http.Request, target any) error {
	defer request.Body.Close()
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON object")
		}
		return err
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func repositoryError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, persistence.ErrNotFound):
		writeError(response, http.StatusNotFound, err)
	case errors.Is(err, persistence.ErrConflict):
		writeError(response, http.StatusConflict, err)
	case errors.Is(err, persistence.ErrInvalidCursor):
		writeError(response, http.StatusBadRequest, err)
	default:
		writeError(response, http.StatusInternalServerError, err)
	}
}
