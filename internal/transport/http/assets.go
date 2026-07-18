package httptransport

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
)

const assetRelationshipNeighborhoodDepth = 3

type assetRelationshipGraphRepository interface {
	ListRelationshipsForAsset(context.Context, asset.ConnectionID, asset.AssetID) ([]graph.Relationship, error)
	ListLifecycleBindingsForAsset(context.Context, asset.ConnectionID, asset.AssetID) ([]graph.LifecycleBinding, error)
	ListRelationshipsByAssetIDs(context.Context, []asset.AssetID) ([]graph.Relationship, error)
	ListLifecycleBindingsByAssetIDs(context.Context, []asset.AssetID) ([]graph.LifecycleBinding, error)
}

func (a *API) listAssets(response http.ResponseWriter, request *http.Request) {
	options := pageOptions(request)
	if err := validateAssetCanvas(options); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "assets.canvas_invalid", Message: err.Error()})
		return
	}
	expression, err := a.parseAssetResourceQuery(request)
	if err != nil {
		var parseError *resourcequery.ParseError
		var validationError *resourcequery.ValidationError
		if errors.As(err, &parseError) || errors.As(err, &validationError) {
			details := map[string]any{}
			if parseError != nil {
				details["position"] = parseError.Position
			}
			if validationError != nil {
				details["position"] = validationError.Position
			}
			writeAPIError(response, http.StatusBadRequest, APIError{Code: "assets.query_invalid", Message: err.Error(), Details: details})
			return
		}
		repositoryError(response, err)
		return
	}
	options.ResourceQuery = expression
	page, err := a.dependencies.Repositories.Inventory().ListAssets(request.Context(), options)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func (a *API) parseAssetResourceQuery(request *http.Request) (*resourcequery.Expression, error) {
	expression, err := resourcequery.Parse(request.URL.Query().Get("resource_query"))
	if err != nil || expression == nil {
		return expression, err
	}
	connection, err := a.dependencies.Repositories.Connections().GetConnection(
		request.Context(),
		selectedConnectionID(request),
	)
	if err != nil {
		return nil, err
	}
	return expression, expression.Validate(a.resourceQueryKinds(connection.Provider))
}

func (a *API) resourceQueryKinds(provider asset.Provider) []asset.ResourceKind {
	if a.dependencies.Bundles == nil {
		return nil
	}
	var result []asset.ResourceKind
	for _, bundle := range a.dependencies.Bundles.Bundles() {
		if bundle.Provider != provider {
			continue
		}
		result = compiledResourceKinds(bundle.Specs)
		break
	}
	if catalog, ok := a.dependencies.Bundles.(ResourceKindCatalog); ok {
		if runtimeKinds, _, constrained := catalog.ResourceKinds(provider); constrained {
			compiledByID := make(map[asset.ResourceKindID]asset.ResourceKind, len(result))
			for _, kind := range result {
				compiledByID[kind.ID] = kind
			}
			result = make([]asset.ResourceKind, 0, len(runtimeKinds))
			for _, runtimeKind := range runtimeKinds {
				if compiled, exists := compiledByID[runtimeKind.ID]; exists && len(runtimeKind.Properties) == 0 {
					runtimeKind.Properties = compiled.Properties
				}
				result = append(result, runtimeKind)
			}
		}
	}
	return result
}

func (a *API) patchAsset(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Dirty *bool `json:"dirty"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "asset.request_invalid", Message: err.Error()})
		return
	}
	if input.Dirty == nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "asset.request_invalid", Message: "dirty is required"})
		return
	}

	id := asset.AssetID(chi.URLParam(request, "id"))
	connectionID := selectedConnectionID(request)
	principal, _ := principalFromContext(request.Context())
	var updated asset.Asset
	err := a.dependencies.Repositories.WithTx(request.Context(), func(repositories persistence.Repositories) error {
		current, err := repositories.Inventory().GetAsset(request.Context(), id)
		if err != nil {
			return err
		}
		if current.Identity.ConnectionID != connectionID {
			return persistence.ErrNotFound
		}
		updated, err = repositories.Inventory().SetAssetDirty(request.Context(), id, *input.Dirty)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		action := "asset.dirty.mark"
		if !*input.Dirty {
			action = "asset.dirty.unmark"
		}
		return repositories.Audits().AppendAuditEvent(request.Context(), execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: connectionID,
			Actor: principal.Subject, Action: action, TargetType: "asset", TargetID: string(id),
			Result: "succeeded", Evidence: map[string]any{"dirty": *input.Dirty}, CreatedAt: now,
		})
	})
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func validateAssetCanvas(options persistence.ListOptions) error {
	switch options.AssetCanvas {
	case "", persistence.AssetCanvasAccount, persistence.AssetCanvasGlobal:
		return nil
	case persistence.AssetCanvasRegion, persistence.AssetCanvasRegionPublic:
		if strings.TrimSpace(options.RegionID) == "" {
			return errors.New("region_id is required for the selected asset canvas")
		}
		return nil
	case persistence.AssetCanvasVPC:
		if strings.TrimSpace(options.RegionID) == "" || strings.TrimSpace(options.VPCID) == "" {
			return errors.New("region_id and vpc_id are required for the VPC asset canvas")
		}
		return nil
	default:
		return errors.New("canvas must be account, global, region, region-public, or vpc")
	}
}

func (a *API) assetGraph(response http.ResponseWriter, request *http.Request) {
	id := asset.AssetID(chi.URLParam(request, "id"))
	relationships, _, err := loadAssetRelationshipNeighborhood(
		request.Context(),
		a.dependencies.Repositories.Graph(),
		selectedConnectionID(request),
		id,
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Relationships []graph.Relationship `json:"relationships"`
	}{Relationships: relationships})
}

func (a *API) assetLifecycle(response http.ResponseWriter, request *http.Request) {
	id := asset.AssetID(chi.URLParam(request, "id"))
	_, bindings, err := loadAssetRelationshipNeighborhood(
		request.Context(),
		a.dependencies.Repositories.Graph(),
		selectedConnectionID(request),
		id,
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Bindings []graph.LifecycleBinding `json:"bindings"`
	}{Bindings: bindings})
}

func loadAssetRelationshipNeighborhood(
	ctx context.Context,
	repository assetRelationshipGraphRepository,
	connectionID asset.ConnectionID,
	focusID asset.AssetID,
) ([]graph.Relationship, []graph.LifecycleBinding, error) {
	directRelationships, err := repository.ListRelationshipsForAsset(ctx, connectionID, focusID)
	if err != nil {
		return nil, nil, err
	}
	directBindings, err := repository.ListLifecycleBindingsForAsset(ctx, connectionID, focusID)
	if err != nil {
		return nil, nil, err
	}

	relationshipsByID := make(map[graph.RelationshipID]graph.Relationship)
	bindingsByID := make(map[graph.LifecycleBindingID]graph.LifecycleBinding)
	seenAssets := map[asset.AssetID]struct{}{focusID: {}}
	frontier := []asset.AssetID{focusID}

	for depth := 0; depth < assetRelationshipNeighborhoodDepth && len(frontier) > 0; depth++ {
		relationships := directRelationships
		bindings := directBindings
		if depth > 0 {
			relationships, err = repository.ListRelationshipsByAssetIDs(ctx, frontier)
			if err != nil {
				return nil, nil, err
			}
			bindings, err = repository.ListLifecycleBindingsByAssetIDs(ctx, frontier)
			if err != nil {
				return nil, nil, err
			}
		}

		frontierSet := make(map[asset.AssetID]struct{}, len(frontier))
		for _, id := range frontier {
			frontierSet[id] = struct{}{}
		}
		next := make([]asset.AssetID, 0)
		for _, relationship := range relationships {
			relationshipsByID[relationship.ID] = relationship
			for _, currentID := range frontier {
				peerID, connected := relationshipPeer(relationship, currentID)
				if !connected {
					continue
				}
				_, known := seenAssets[peerID]
				seenAssets[peerID] = struct{}{}
				if !known &&
					depth+1 < assetRelationshipNeighborhoodDepth &&
					!relationshipParentPeer(relationship, currentID, peerID) {
					next = append(next, peerID)
				}
			}
		}
		for _, binding := range bindings {
			bindingsByID[binding.ID] = binding
			for _, currentID := range frontier {
				peerID, connected := lifecyclePeer(binding, currentID)
				if !connected {
					continue
				}
				if _, known := seenAssets[peerID]; !known {
					seenAssets[peerID] = struct{}{}
					if depth+1 < assetRelationshipNeighborhoodDepth {
						next = append(next, peerID)
					}
				}
			}
		}
		frontier = uniqueAssetIDs(next, frontierSet)
	}

	relationships := make([]graph.Relationship, 0, len(relationshipsByID))
	for _, relationship := range relationshipsByID {
		relationships = append(relationships, relationship)
	}
	sort.Slice(relationships, func(i, j int) bool {
		return relationships[i].ID < relationships[j].ID
	})
	bindings := make([]graph.LifecycleBinding, 0, len(bindingsByID))
	for _, binding := range bindingsByID {
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].ID < bindings[j].ID
	})
	return relationships, bindings, nil
}

func relationshipPeer(
	relationship graph.Relationship,
	currentID asset.AssetID,
) (asset.AssetID, bool) {
	switch currentID {
	case relationship.SourceAssetID:
		return relationship.TargetAssetID, true
	case relationship.TargetAssetID:
		return relationship.SourceAssetID, true
	default:
		return "", false
	}
}

func lifecyclePeer(
	binding graph.LifecycleBinding,
	currentID asset.AssetID,
) (asset.AssetID, bool) {
	switch currentID {
	case binding.ControllerAssetID:
		return binding.ManagedAssetID, true
	case binding.ManagedAssetID:
		return binding.ControllerAssetID, true
	default:
		return "", false
	}
}

func relationshipParentPeer(
	relationship graph.Relationship,
	currentID asset.AssetID,
	peerID asset.AssetID,
) bool {
	return relationship.Type == graph.RelationshipMemberOf &&
		relationship.SourceAssetID == currentID &&
		relationship.TargetAssetID == peerID
}

func uniqueAssetIDs(values []asset.AssetID, exclude map[asset.AssetID]struct{}) []asset.AssetID {
	seen := make(map[asset.AssetID]struct{}, len(values))
	result := make([]asset.AssetID, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, skip := exclude[value]; skip {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (a *API) listScopes(response http.ResponseWriter, request *http.Request) {
	page, err := a.dependencies.Repositories.Inventory().ListScopes(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func (a *API) listFindings(response http.ResponseWriter, request *http.Request) {
	assetID := asset.AssetID(request.URL.Query().Get("asset_id"))
	if assetID != "" {
		values, err := a.dependencies.Repositories.Findings().ListFindingsForAsset(
			request.Context(), selectedConnectionID(request), assetID,
		)
		if err != nil {
			repositoryError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, persistence.Page[finding.Finding]{Items: values})
		return
	}
	page, err := a.dependencies.Repositories.Findings().ListFindings(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}
