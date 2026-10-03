package scancoverage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

const CandidateLimit = 100

type Requirement struct {
	RegionIDs  []string
	Global     bool
	Unprovable bool
}

type ConnectionSummary struct {
	Status             string
	FailedShards       int
	LastCompleteScanAt *time.Time
}

func EvaluateConnection(
	ctx context.Context,
	repository persistence.InventoryRepository,
	connectionID asset.ConnectionID,
	requirement Requirement,
) (ConnectionSummary, error) {
	if requirement.Unprovable {
		return ConnectionSummary{Status: "incomplete"}, nil
	}
	candidates, err := listCandidates(ctx, repository, connectionID)
	if err != nil {
		return ConnectionSummary{}, err
	}
	return evaluateCandidates(ctx, repository, candidates, requirement)
}

// summaryLimit bounds the remembered connections; at the limit the memo
// simply starts over.
const summaryLimit = 256

// Evaluator evaluates coverage like EvaluateConnection, but remembers each
// connection's summary together with a digest of the scan runs and the
// requirement it was computed from. Any new, finished, retried or deleted
// scan changes the run list and therefore the digest, so only an unchanged
// history skips rereading the shards of failed or skipped scans.
type Evaluator struct {
	mu        sync.Mutex
	summaries map[asset.ConnectionID]rememberedSummary
}

type rememberedSummary struct {
	digest  [sha256.Size]byte
	summary ConnectionSummary
}

func NewEvaluator() *Evaluator {
	return &Evaluator{summaries: make(map[asset.ConnectionID]rememberedSummary)}
}

func (e *Evaluator) EvaluateConnection(
	ctx context.Context,
	repository persistence.InventoryRepository,
	connectionID asset.ConnectionID,
	requirement Requirement,
) (ConnectionSummary, error) {
	if requirement.Unprovable {
		return ConnectionSummary{Status: "incomplete"}, nil
	}
	candidates, err := listCandidates(ctx, repository, connectionID)
	if err != nil {
		return ConnectionSummary{}, err
	}
	payload, err := json.Marshal(struct {
		Runs        []asset.ScanRun
		Requirement Requirement
	}{candidates, requirement})
	if err != nil {
		return evaluateCandidates(ctx, repository, candidates, requirement)
	}
	digest := sha256.Sum256(payload)
	e.mu.Lock()
	remembered, ok := e.summaries[connectionID]
	e.mu.Unlock()
	if ok && remembered.digest == digest {
		return cloneSummary(remembered.summary), nil
	}
	summary, err := evaluateCandidates(ctx, repository, candidates, requirement)
	if err != nil {
		return ConnectionSummary{}, err
	}
	e.mu.Lock()
	if len(e.summaries) >= summaryLimit {
		clear(e.summaries)
	}
	e.summaries[connectionID] = rememberedSummary{digest: digest, summary: cloneSummary(summary)}
	e.mu.Unlock()
	return summary, nil
}

func cloneSummary(summary ConnectionSummary) ConnectionSummary {
	if summary.LastCompleteScanAt != nil {
		finished := *summary.LastCompleteScanAt
		summary.LastCompleteScanAt = &finished
	}
	return summary
}

func listCandidates(
	ctx context.Context,
	repository persistence.InventoryRepository,
	connectionID asset.ConnectionID,
) ([]asset.ScanRun, error) {
	page, err := repository.ListScanRuns(ctx, persistence.ListOptions{
		ConnectionID: connectionID,
		Limit:        CandidateLimit,
	})
	if err != nil {
		return nil, err
	}
	candidates := page.Items
	if len(candidates) > CandidateLimit {
		candidates = candidates[:CandidateLimit]
	}
	return candidates, nil
}

func evaluateCandidates(
	ctx context.Context,
	repository persistence.InventoryRepository,
	candidates []asset.ScanRun,
	requirement Requirement,
) (ConnectionSummary, error) {
	result := ConnectionSummary{Status: "unknown"}
	required := requiredTargetSet(requirement)
	if len(candidates) > 0 {
		result.Status = "incomplete"
	}
	for _, run := range candidates {
		targets, ok := applicableRun(run, required)
		if !ok {
			continue
		}
		shards, err := repository.ListScanShardsByRun(ctx, run.ID)
		if err != nil {
			return ConnectionSummary{}, err
		}
		failed, complete := verifiedShards(shards, targets)
		if result.FailedShards == 0 {
			result.FailedShards = failed
		}
		if !complete {
			continue
		}
		finished := *run.FinishedAt
		result.Status = "complete"
		result.LastCompleteScanAt = &finished
		return result, nil
	}
	return result, nil
}

type declaredTarget struct {
	kind     asset.ScanTargetKind
	regionID string
}

func applicableRun(run asset.ScanRun, required map[string]struct{}) (map[string]declaredTarget, bool) {
	if run.Status != asset.ScanSucceeded ||
		run.FinishedAt == nil ||
		run.ScopeMode != asset.ScanAllActiveRegions ||
		len(run.ResourceKindIDs) != 0 {
		return nil, false
	}
	covered := make(map[string]struct{}, len(run.Targets))
	targets := make(map[string]declaredTarget, len(run.Targets))
	for _, target := range run.Targets {
		key := strings.TrimSpace(target.Key)
		if key == "" {
			return nil, false
		}
		if _, duplicate := targets[key]; duplicate {
			return nil, false
		}
		var identity string
		switch target.Kind {
		case asset.ScanTargetRegion:
			regionID := strings.TrimSpace(target.RegionID)
			if regionID == "" || regionID == "global" {
				return nil, false
			}
			identity = "region:" + regionID
			targets[key] = declaredTarget{kind: target.Kind, regionID: regionID}
		case asset.ScanTargetGlobal:
			identity = "global"
			targets[key] = declaredTarget{kind: target.Kind, regionID: "global"}
		default:
			return nil, false
		}
		if _, duplicate := covered[identity]; duplicate {
			return nil, false
		}
		covered[identity] = struct{}{}
	}
	for identity := range required {
		if _, ok := covered[identity]; !ok {
			return nil, false
		}
	}
	if len(targets) == 0 {
		return nil, false
	}
	return targets, true
}

func verifiedShards(shards []asset.ScanShard, targets map[string]declaredTarget) (int, bool) {
	if len(shards) == 0 {
		return 0, false
	}
	failed := 0
	complete := true
	counts := make(map[string]int, len(targets))
	seen := make(map[string]struct{}, len(shards))
	for _, shard := range shards {
		target, declared := targets[strings.TrimSpace(shard.TargetKey)]
		if !declared {
			complete = false
			continue
		}
		regionID := strings.TrimSpace(shard.RegionID)
		if target.kind == asset.ScanTargetRegion && regionID != target.regionID {
			complete = false
		}
		if target.kind == asset.ScanTargetGlobal && regionID != "" && regionID != "global" {
			complete = false
		}
		if coverageTarget := strings.TrimSpace(shard.Coverage.TargetKey); coverageTarget != "" && coverageTarget != strings.TrimSpace(shard.TargetKey) {
			complete = false
		}
		signature := strings.Join([]string{
			strings.TrimSpace(shard.TargetKey), strings.TrimSpace(shard.Source),
			string(shard.ScopeID), string(shard.ResourceKindID),
		}, "\x00")
		if _, duplicate := seen[signature]; duplicate {
			complete = false
		}
		seen[signature] = struct{}{}
		counts[strings.TrimSpace(shard.TargetKey)]++
		if shard.Status == asset.ShardFailed {
			failed++
		}
		if shard.Status != asset.ShardSucceeded || !shard.Coverage.Complete {
			complete = false
		}
	}
	for key := range targets {
		if counts[key] == 0 {
			complete = false
		}
	}
	return failed, complete
}

func ActiveRegionRequirement(regions []asset.ConnectionRegion, connectionID asset.ConnectionID) Requirement {
	result := Requirement{}
	for _, region := range regions {
		if region.ConnectionID != connectionID || region.Lifecycle != asset.RegionActive {
			continue
		}
		if regionID := strings.TrimSpace(region.RegionID); regionID != "" {
			result.RegionIDs = append(result.RegionIDs, regionID)
		}
	}
	return result
}

func requiredTargetSet(requirement Requirement) map[string]struct{} {
	result := make(map[string]struct{}, len(requirement.RegionIDs)+1)
	for _, regionID := range requirement.RegionIDs {
		if regionID := strings.TrimSpace(regionID); regionID != "" && regionID != "global" {
			result["region:"+regionID] = struct{}{}
		}
	}
	if requirement.Global {
		result["global"] = struct{}{}
	}
	return result
}
