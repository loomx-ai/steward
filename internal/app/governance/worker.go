package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type BundleDirectory interface {
	Bundle(asset.Provider) (spec.Bundle, error)
}

type ContributorResolver interface {
	ResolveContributors(context.Context, asset.CloudConnection, []asset.Asset) ([]Contributor, error)
}

type GraphHandler struct {
	repositories persistence.Repositories
	bundles      BundleDirectory
	contributors ContributorResolver
	graphs       *Service
	findings     *FindingEngine
	clock        func() time.Time
}

func NewGraphHandler(repositories persistence.Repositories, bundles BundleDirectory, contributors ContributorResolver) *GraphHandler {
	handler := &GraphHandler{
		repositories: repositories,
		bundles:      bundles,
		contributors: contributors,
		clock:        func() time.Time { return time.Now().UTC() },
	}
	if repositories != nil {
		handler.graphs = NewService(repositories.Inventory(), repositories.Graph())
		handler.findings = NewFindingEngine(repositories.Findings())
	}
	return handler
}

func (h *GraphHandler) Handle(ctx context.Context, job execution.Job) (handleErr error) {
	if h == nil || h.repositories == nil || h.bundles == nil || h.graphs == nil || h.findings == nil {
		return fmt.Errorf("graph handler requires repositories, bundle directory, graph service, and finding engine")
	}
	runID, _ := job.Payload["scan_run_id"].(string)
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("graph job requires scan_run_id")
	}
	run, err := h.repositories.Inventory().GetScanRun(ctx, asset.ScanRunID(runID))
	if err != nil {
		return err
	}
	reconciling := run.Status == asset.ScanReconciling
	completionStatus := run.CompletionStatus
	if !reconciling && run.Status != asset.ScanSucceeded && run.Status != asset.ScanPartial {
		return fmt.Errorf("scan run %q is not eligible for graph rebuild: %s", run.ID, run.Status)
	}
	if reconciling {
		defer func() {
			if handleErr == nil {
				return
			}
			run.Status = asset.ScanFailed
			run.CompletionStatus = completionStatus
			finishedAt := h.clock()
			run.FinishedAt = &finishedAt
			if err := h.repositories.Inventory().PutScanRun(ctx, run); err != nil {
				handleErr = errors.Join(handleErr, fmt.Errorf("mark scan run %q failed after graph reconciliation: %w", run.ID, err))
			}
		}()
		if run.CompletionStatus != asset.ScanSucceeded && run.CompletionStatus != asset.ScanPartial {
			return fmt.Errorf("scan run %q has invalid graph completion status %q", run.ID, run.CompletionStatus)
		}
	}
	connection, err := h.repositories.Connections().GetConnection(ctx, run.ConnectionID)
	if err != nil {
		return err
	}
	shards, err := h.scanShards(ctx, run.ID)
	if err != nil {
		return err
	}
	rootScopeID, err := h.rootScope(ctx, connection.ID, shards)
	if err != nil {
		return err
	}
	assets, err := h.repositories.Inventory().ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil {
		return err
	}
	bundle, err := h.bundles.Bundle(connection.Provider)
	if err != nil {
		return err
	}
	var contributors []Contributor
	if h.contributors != nil {
		contributors, err = h.contributors.ResolveContributors(ctx, connection, assets)
		if err != nil {
			return err
		}
	}
	if _, err := h.graphs.RebuildGraphFromAssets(ctx, rootScopeID, connection.ID, string(run.ID), bundle, contributors, assets); err != nil {
		return err
	}
	if err := h.evaluateFindings(ctx, run, shards, assets, bundle); err != nil {
		return err
	}
	if reconciling {
		run.Status = completionStatus
		run.CompletionStatus = ""
		finishedAt := h.clock()
		run.FinishedAt = &finishedAt
		return h.repositories.Inventory().PutScanRun(ctx, run)
	}
	return nil
}

func (h *GraphHandler) scanShards(ctx context.Context, runID asset.ScanRunID) ([]asset.ScanShard, error) {
	result, err := h.repositories.Inventory().ListScanShardsByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("scan run %q has no shards", runID)
	}
	return result, nil
}

func (h *GraphHandler) rootScope(ctx context.Context, connectionID asset.ConnectionID, shards []asset.ScanShard) (asset.ScopeID, error) {
	roots := make(map[asset.ScopeID]struct{})
	checked := make(map[asset.ScopeID]struct{})
	for _, shard := range shards {
		// Thousands of shards share a few scopes; walk each scope once.
		if _, done := checked[shard.ScopeID]; done {
			continue
		}
		checked[shard.ScopeID] = struct{}{}
		current, err := h.repositories.Inventory().GetScope(ctx, shard.ScopeID)
		if err != nil {
			return "", err
		}
		visited := make(map[asset.ScopeID]struct{})
		for current.ParentID != "" {
			if current.ConnectionID != connectionID {
				return "", fmt.Errorf("scope %q does not belong to connection %q", current.ID, connectionID)
			}
			if _, exists := visited[current.ID]; exists {
				return "", fmt.Errorf("scope hierarchy contains a cycle at %q", current.ID)
			}
			visited[current.ID] = struct{}{}
			current, err = h.repositories.Inventory().GetScope(ctx, current.ParentID)
			if err != nil {
				return "", err
			}
		}
		if current.ConnectionID != connectionID {
			return "", fmt.Errorf("root scope %q does not belong to connection %q", current.ID, connectionID)
		}
		roots[current.ID] = struct{}{}
	}
	if len(roots) != 1 {
		return "", fmt.Errorf("scan run spans %d connection roots; expected exactly one", len(roots))
	}
	for root := range roots {
		return root, nil
	}
	return "", fmt.Errorf("scan run has no root scope")
}

func (h *GraphHandler) evaluateFindings(ctx context.Context, run asset.ScanRun, shards []asset.ScanShard, assets []asset.Asset, bundle spec.Bundle) error {
	byNativeType := make(map[string]spec.CompiledSpec, len(bundle.Specs))
	for _, compiled := range bundle.Specs {
		byNativeType[compiled.ResourceKind.NativeType] = compiled
	}
	observedAt := time.Now().UTC()
	if run.FinishedAt != nil {
		observedAt = *run.FinishedAt
	}
	ids, err := h.repositories.Inventory().ListAssetIDsObservedByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	observedAssetIDs := make(map[asset.AssetID]struct{}, len(ids))
	for _, id := range ids {
		observedAssetIDs[id] = struct{}{}
	}
	// Only shards of the asset's kind or of every kind can cover it.
	shardsByKind := make(map[asset.ResourceKindID][]asset.ScanShard)
	for _, shard := range shards {
		shardsByKind[shard.ResourceKindID] = append(shardsByKind[shard.ResourceKindID], shard)
	}
	ancestorCache := make(map[asset.ScopeID]map[asset.ScopeID]struct{})
	// Coverage depends only on an asset's scope and kind, which many share.
	type coverageKey struct {
		scope asset.ScopeID
		kind  asset.ResourceKindID
	}
	coverage := make(map[coverageKey]bool)
	var evaluations []AssetEvaluation
	for _, value := range assets {
		if _, observed := observedAssetIDs[value.ID]; !observed {
			continue
		}
		compiled, exists := byNativeType[value.Identity.NativeType]
		if !exists || len(compiled.Rules) == 0 {
			continue
		}
		key := coverageKey{value.ScopeID, value.ResourceKindID}
		authoritativeComplete, cached := coverage[key]
		if !cached {
			if authoritativeComplete, err = h.authoritativeCoverage(ctx, value, shardsByKind, ancestorCache); err != nil {
				return err
			}
			coverage[key] = authoritativeComplete
		}
		evaluations = append(evaluations, AssetEvaluation{Asset: value, Compiled: compiled, Evaluation: Evaluation{
			ObservedAt: observedAt, Authoritative: authoritativeComplete, Complete: authoritativeComplete,
		}})
	}
	return h.findings.EvaluateAll(ctx, evaluations)
}

func (h *GraphHandler) authoritativeCoverage(ctx context.Context, value asset.Asset, shardsByKind map[asset.ResourceKindID][]asset.ScanShard, cache map[asset.ScopeID]map[asset.ScopeID]struct{}) (bool, error) {
	if value.ScopeID == "" || value.ResourceKindID == "" {
		return false, nil
	}
	ancestors, err := h.scopeAncestors(ctx, value.ScopeID, cache)
	if err != nil {
		return false, err
	}
	seen := false
	complete := true
	for _, shards := range [2][]asset.ScanShard{shardsByKind[""], shardsByKind[value.ResourceKindID]} {
		for _, shard := range shards {
			if _, covers := ancestors[shard.ScopeID]; !covers {
				continue
			}
			seen = true
			complete = complete && shard.Status == asset.ShardSucceeded && shard.Authoritative && shard.Coverage.Authoritative && shard.Coverage.Complete
		}
	}
	return seen && complete, nil
}

func (h *GraphHandler) scopeAncestors(ctx context.Context, scopeID asset.ScopeID, cache map[asset.ScopeID]map[asset.ScopeID]struct{}) (map[asset.ScopeID]struct{}, error) {
	if cached, exists := cache[scopeID]; exists {
		return cached, nil
	}
	result := make(map[asset.ScopeID]struct{})
	currentID := scopeID
	for currentID != "" {
		if _, exists := result[currentID]; exists {
			return nil, fmt.Errorf("scope hierarchy contains a cycle at %q", currentID)
		}
		result[currentID] = struct{}{}
		current, err := h.repositories.Inventory().GetScope(ctx, currentID)
		if err != nil {
			return nil, err
		}
		currentID = current.ParentID
	}
	cache[scopeID] = result
	return result, nil
}
