package scancoverage

import (
	"context"
	"strings"
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
	result := ConnectionSummary{Status: "unknown"}
	if requirement.Unprovable {
		return ConnectionSummary{Status: "incomplete"}, nil
	}
	required := requiredTargetSet(requirement)
	page, err := repository.ListScanRuns(ctx, persistence.ListOptions{
		ConnectionID: connectionID,
		Limit:        CandidateLimit,
	})
	if err != nil {
		return ConnectionSummary{}, err
	}
	if len(page.Items) > 0 {
		result.Status = "incomplete"
	}
	candidates := page.Items
	if len(candidates) > CandidateLimit {
		candidates = candidates[:CandidateLimit]
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
