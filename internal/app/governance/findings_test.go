package governance_test

import (
	"context"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type findingRepository struct {
	findings map[finding.ID]finding.Finding
}

func (r *findingRepository) WithinFindingTx(ctx context.Context, fn func(persistence.FindingRepository) error) error {
	return fn(r)
}

func (r *findingRepository) PutFinding(_ context.Context, value finding.Finding) error {
	r.findings[value.ID] = value
	return nil
}

func (r *findingRepository) ListFindingsByAsset(_ context.Context, assetID asset.AssetID) ([]finding.Finding, error) {
	var result []finding.Finding
	for _, value := range r.findings {
		if value.AssetID == assetID {
			result = append(result, value)
		}
	}
	return result, nil
}

func (r *findingRepository) ListFindingsForAsset(ctx context.Context, _ asset.ConnectionID, assetID asset.AssetID) ([]finding.Finding, error) {
	return r.ListFindingsByAsset(ctx, assetID)
}

func (r *findingRepository) CountOpenFindingsByAssetIDs(_ context.Context, assetIDs []asset.AssetID) (map[asset.AssetID]int, error) {
	wanted := make(map[asset.AssetID]struct{}, len(assetIDs))
	for _, id := range assetIDs {
		wanted[id] = struct{}{}
	}
	result := make(map[asset.AssetID]int)
	for _, value := range r.findings {
		if _, ok := wanted[value.AssetID]; ok && value.Status == finding.StatusOpen && value.ClosedAt == nil {
			result[value.AssetID]++
		}
	}
	return result, nil
}

func (r *findingRepository) ListFindings(context.Context, persistence.ListOptions) (persistence.Page[finding.Finding], error) {
	result := make([]finding.Finding, 0, len(r.findings))
	for _, value := range r.findings {
		result = append(result, value)
	}
	return persistence.Page[finding.Finding]{Items: result}, nil
}

func TestFindingEngineUsesOnlyNormalizedFieldsAndClosesStaleResults(t *testing.T) {
	t.Parallel()

	repository := &findingRepository{findings: make(map[finding.ID]finding.Finding)}
	engine := governance.NewFindingEngine(repository)
	compiled := spec.CompiledSpec{
		Revision: "spec-revision-1",
		Rules: []spec.CompiledRule{{
			ID: "stopped-instance", Title: "Stopped instance", Severity: finding.SeverityHigh,
			FieldPath: []string{"state"}, Operator: spec.RuleEqual, Expected: "Stopped",
		}},
	}
	observedAt := time.Date(2026, 7, 13, 5, 0, 0, 0, time.UTC)
	value := asset.Asset{ID: "asset-1", Normalized: map[string]any{"state": "Running"}}
	if err := engine.Evaluate(context.Background(), value, compiled, governance.Evaluation{ObservedAt: observedAt, Authoritative: true, Complete: true}); err != nil {
		t.Fatalf("evaluate normalized non-match: %v", err)
	}
	if len(repository.findings) != 0 {
		t.Fatalf("finding engine consumed raw provider data: %+v", repository.findings)
	}
	value.Normalized["state"] = "Stopped"
	if err := engine.Evaluate(context.Background(), value, compiled, governance.Evaluation{ObservedAt: observedAt.Add(time.Minute), Authoritative: true, Complete: true}); err != nil {
		t.Fatalf("evaluate normalized match: %v", err)
	}
	if len(repository.findings) != 1 {
		t.Fatalf("findings = %+v", repository.findings)
	}
	for _, result := range repository.findings {
		if result.SpecBundleRevision != "spec-revision-1" || result.RuleID != "stopped-instance" || result.Evidence["actual"] != "Stopped" {
			t.Fatalf("finding lost revision or evidence: %+v", result)
		}
	}
	value.Normalized["state"] = "Running"
	if err := engine.Evaluate(context.Background(), value, compiled, governance.Evaluation{ObservedAt: observedAt.Add(2 * time.Minute), Authoritative: false, Complete: false}); err != nil {
		t.Fatalf("partial reevaluation: %v", err)
	}
	for _, result := range repository.findings {
		if result.Status != finding.StatusOpen {
			t.Fatalf("partial reevaluation closed finding: %+v", result)
		}
	}
	if err := engine.Evaluate(context.Background(), value, compiled, governance.Evaluation{ObservedAt: observedAt.Add(3 * time.Minute), Authoritative: true, Complete: true}); err != nil {
		t.Fatalf("authoritative reevaluation: %v", err)
	}
	for _, result := range repository.findings {
		if result.Status != finding.StatusClosed || result.ClosedAt == nil {
			t.Fatalf("stale finding not closed: %+v", result)
		}
	}
}
