package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const findingEngine = "spec-governance"

type Evaluation struct {
	ObservedAt    time.Time
	Authoritative bool
	Complete      bool
}

type FindingEngine struct {
	repository persistence.FindingRepository
}

func NewFindingEngine(repository persistence.FindingRepository) *FindingEngine {
	return &FindingEngine{repository: repository}
}

// Evaluate receives only the normalized Asset projection and compiled rules.
// Provider raw payloads and provider action runtimes are intentionally absent
// from this boundary.
func (e *FindingEngine) Evaluate(ctx context.Context, value asset.Asset, compiled spec.CompiledSpec, evaluation Evaluation) error {
	if value.ID == "" {
		return fmt.Errorf("finding evaluation requires an asset")
	}
	if evaluation.ObservedAt.IsZero() {
		return fmt.Errorf("finding evaluation requires observed time")
	}
	return e.repository.WithinFindingTx(ctx, func(repository persistence.FindingRepository) error {
		existing, err := repository.ListFindingsByAsset(ctx, value.ID)
		if err != nil {
			return err
		}
		return repository.PutFindings(ctx, evaluate(value, compiled, evaluation, existing))
	})
}

// AssetEvaluation is one asset's input to EvaluateAll.
type AssetEvaluation struct {
	Asset      asset.Asset
	Compiled   spec.CompiledSpec
	Evaluation Evaluation
}

// findingTxChunk bounds how many assets share one finding transaction so a
// large scan neither commits per asset nor holds one long write lock.
const findingTxChunk = 250

// EvaluateAll evaluates every item as Evaluate would, committing a chunk of
// assets per transaction instead of one transaction per asset.
func (e *FindingEngine) EvaluateAll(ctx context.Context, items []AssetEvaluation) error {
	for _, item := range items {
		if item.Asset.ID == "" {
			return fmt.Errorf("finding evaluation requires an asset")
		}
		if item.Evaluation.ObservedAt.IsZero() {
			return fmt.Errorf("finding evaluation requires observed time")
		}
	}
	for start := 0; start < len(items); start += findingTxChunk {
		chunk := items[start:min(start+findingTxChunk, len(items))]
		if err := e.repository.WithinFindingTx(ctx, func(repository persistence.FindingRepository) error {
			ids := make([]asset.AssetID, len(chunk))
			for index, item := range chunk {
				ids[index] = item.Asset.ID
			}
			prefetched, err := repository.ListFindingsByAssetIDs(ctx, ids)
			if err != nil {
				return err
			}
			evaluated := make(map[asset.AssetID]struct{}, len(chunk))
			var pending []finding.Finding
			for _, item := range chunk {
				existing := prefetched[item.Asset.ID]
				// A repeated asset must see what its earlier item wrote.
				if _, repeated := evaluated[item.Asset.ID]; repeated {
					if err := repository.PutFindings(ctx, pending); err != nil {
						return err
					}
					pending = nil
					if existing, err = repository.ListFindingsByAsset(ctx, item.Asset.ID); err != nil {
						return err
					}
				}
				evaluated[item.Asset.ID] = struct{}{}
				pending = append(pending, evaluate(item.Asset, item.Compiled, item.Evaluation, existing)...)
			}
			return repository.PutFindings(ctx, pending)
		}); err != nil {
			return err
		}
	}
	return nil
}

// evaluate returns the findings to write for one asset, in write order.
func evaluate(value asset.Asset, compiled spec.CompiledSpec, evaluation Evaluation, existing []finding.Finding) []finding.Finding {
	var writes []finding.Finding
	byRule := make(map[string]finding.Finding, len(existing))
	for _, current := range existing {
		if current.Evidence["engine"] == findingEngine {
			byRule[current.RuleID] = current
		}
	}
	matched := make(map[string]struct{}, len(compiled.Rules))
	for _, rule := range compiled.Rules {
		actual, ok := normalizedValue(value.Normalized, rule.FieldPath)
		if !ok || !ruleMatches(actual, rule.Operator, rule.Expected) {
			continue
		}
		matched[rule.ID] = struct{}{}
		result, exists := byRule[rule.ID]
		if !exists {
			result = finding.Finding{
				ID: finding.ID(idgen.MustNew("fnd")), AssetID: value.ID, RuleID: rule.ID,
				FirstSeenAt: evaluation.ObservedAt,
			}
		}
		result.Status = finding.StatusOpen
		result.Severity = rule.Severity
		result.Title = rule.Title
		result.Description = rule.Description
		result.SpecBundleRevision = compiled.ResourceKind.BundleRevision
		if result.SpecBundleRevision == "" {
			result.SpecBundleRevision = compiled.Revision
		}
		result.LastSeenAt = evaluation.ObservedAt
		result.ClosedAt = nil
		result.Evidence = map[string]any{
			"engine": findingEngine, "field": strings.Join(rule.FieldPath, "."),
			"operator": string(rule.Operator), "expected": rule.Expected, "actual": actual, "spec_hash": compiled.Hash,
		}
		writes = append(writes, result)
	}
	if !evaluation.Authoritative || !evaluation.Complete {
		return writes
	}
	for _, current := range existing {
		if current.Evidence["engine"] != findingEngine || current.Status != finding.StatusOpen {
			continue
		}
		if _, ok := matched[current.RuleID]; ok {
			continue
		}
		closedAt := evaluation.ObservedAt
		current.Status = finding.StatusClosed
		current.ClosedAt = &closedAt
		current.LastSeenAt = evaluation.ObservedAt
		writes = append(writes, current)
	}
	return writes
}

func normalizedValue(document map[string]any, path []string) (any, bool) {
	var current any = document
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func ruleMatches(actual any, operator spec.RuleOperator, expected any) bool {
	if left, leftOK := numberValue(actual); leftOK {
		if right, rightOK := numberValue(expected); rightOK {
			switch operator {
			case spec.RuleEqual:
				return left == right
			case spec.RuleNotEqual:
				return left != right
			case spec.RuleGreaterThan:
				return left > right
			case spec.RuleGreaterEqual:
				return left >= right
			case spec.RuleLessThan:
				return left < right
			case spec.RuleLessEqual:
				return left <= right
			}
		}
	}
	switch operator {
	case spec.RuleEqual:
		return reflect.DeepEqual(actual, expected)
	case spec.RuleNotEqual:
		return !reflect.DeepEqual(actual, expected)
	default:
		return false
	}
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}
