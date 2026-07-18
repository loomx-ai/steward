package region

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var (
	ErrRegionNotFound   = errors.New("connection region not found")
	ErrRegionConflict   = errors.New("connection region already exists")
	ErrRegionInactive   = errors.New("connection region lifecycle does not allow this operation")
	ErrInvalidDiscovery = errors.New("region discovery result is incomplete")
	ErrRegionNameEmpty  = errors.New("region name is empty")
	ErrRegionIDEmpty    = errors.New("region ID is empty")
	ErrRegionDiscovery  = errors.New("region discovery failed")
	ErrNoActiveRegions  = errors.New("connection has no active regions")
)

type Summary struct {
	Added    int `json:"added,omitempty"`
	Updated  int `json:"updated,omitempty"`
	Missing  int `json:"missing,omitempty"`
	Active   int `json:"active"`
	Retired  int `json:"retired"`
	Excluded int `json:"excluded"`
}

type Service struct {
	repositories persistence.Repositories
	now          func() time.Time
	newID        func() string
}

func NewService(repositories persistence.Repositories) (*Service, error) {
	if repositories == nil {
		return nil, fmt.Errorf("region repositories are required")
	}
	return &Service{
		repositories: repositories,
		now:          func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) entityID(prefix string) string {
	if s.newID != nil {
		return s.newID()
	}
	return idgen.MustNew(prefix)
}

func (s *Service) Refresh(ctx context.Context, connectionID asset.ConnectionID, discovered []contracts.DiscoveredRegion, actor, requestID string) (Summary, error) {
	normalized, err := normalizeDiscovery(discovered)
	if err != nil {
		return Summary{}, err
	}
	if err := s.ensureConnection(ctx, s.repositories, connectionID); err != nil {
		return Summary{}, err
	}

	now := s.now()
	var summary Summary
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := s.ensureConnection(ctx, repositories, connectionID); err != nil {
			return err
		}
		existing, err := repositories.Regions().ListRegionsByConnection(ctx, connectionID)
		if err != nil {
			return err
		}
		byRegionID := regionMap(existing)
		seen := make(map[string]struct{}, len(normalized))
		for _, discoveredRegion := range normalized {
			seen[discoveredRegion.RegionID] = struct{}{}
			region, exists := byRegionID[discoveredRegion.RegionID]
			expectedRevision := region.Revision
			if !exists {
				firstSeenAt := now
				lastSeenAt := now
				region = asset.ConnectionRegion{
					ID: s.entityID("rgn"), ConnectionID: connectionID, RegionID: discoveredRegion.RegionID,
					DiscoveredName: discoveredRegion.Name, Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive,
					FirstSeenAt: &firstSeenAt, LastSeenAt: &lastSeenAt, CreatedAt: now, UpdatedAt: now,
				}
				summary.Added++
			} else {
				region.DiscoveredName = discoveredRegion.Name
				if region.FirstSeenAt == nil {
					firstSeenAt := now
					region.FirstSeenAt = &firstSeenAt
				}
				lastSeenAt := now
				region.LastSeenAt = &lastSeenAt
				region.UpdatedAt = now
				summary.Updated++
			}
			if shouldRetireOnRefresh(discoveredRegion) {
				region.Lifecycle = asset.RegionRetired
			}
			var putErr error
			if exists {
				putErr = repositories.Regions().PutRegionIfUnchanged(ctx, region, expectedRevision)
				region.Revision = expectedRevision + 1
			} else {
				putErr = repositories.Regions().PutRegion(ctx, region)
				region.Revision = 1
			}
			if putErr != nil {
				return mapConflict(putErr)
			}
			byRegionID[region.RegionID] = region
		}
		for regionID := range byRegionID {
			if _, ok := seen[regionID]; !ok {
				summary.Missing++
			}
		}
		summary.addLifecycleCounts(byRegionID)
		evidence := summaryEvidence(summary)
		if requestID = strings.TrimSpace(requestID); requestID != "" {
			evidence["provider_request_id"] = requestID
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(s.entityID("aud")), ConnectionID: connectionID, Actor: normalizedActor(actor),
			Action: "region.refresh", TargetType: "cloud_connection", TargetID: string(connectionID), Result: "updated",
			Evidence: evidence, CreatedAt: now,
		})
	})
	if err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (s *Service) Add(ctx context.Context, connectionID asset.ConnectionID, regionID, name, actor string) (asset.ConnectionRegion, error) {
	regionID = strings.TrimSpace(regionID)
	if regionID == "" {
		return asset.ConnectionRegion{}, ErrRegionIDEmpty
	}
	name = strings.TrimSpace(name)
	now := s.now()
	var created asset.ConnectionRegion
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := s.ensureConnection(ctx, repositories, connectionID); err != nil {
			return err
		}
		if _, err := findRegion(ctx, repositories, connectionID, regionID); err == nil {
			return ErrRegionConflict
		} else if !errors.Is(err, ErrRegionNotFound) {
			return err
		}
		if name == "" {
			return ErrRegionNameEmpty
		}
		created = asset.ConnectionRegion{
			ID: s.entityID("rgn"), ConnectionID: connectionID, RegionID: regionID, NameOverride: name,
			Origin: asset.RegionOriginManual, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := repositories.Regions().PutRegion(ctx, created); err != nil {
			return mapConflict(err)
		}
		return s.appendManualAudit(ctx, repositories, created, actor, "region.add", map[string]any{
			"origin": created.Origin, "lifecycle": created.Lifecycle, "name_override": created.NameOverride,
		})
	})
	return created, err
}

func (s *Service) Rename(ctx context.Context, connectionID asset.ConnectionID, regionID, name, actor string) (asset.ConnectionRegion, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return asset.ConnectionRegion{}, ErrRegionNameEmpty
	}
	return s.mutate(ctx, connectionID, regionID, actor, "region.rename", func(region *asset.ConnectionRegion) (map[string]any, error) {
		old := region.NameOverride
		region.NameOverride = name
		return map[string]any{"old_name_override": old, "new_name_override": name}, nil
	})
}

func (s *Service) ResetName(ctx context.Context, connectionID asset.ConnectionID, regionID, actor string) (asset.ConnectionRegion, error) {
	return s.mutate(ctx, connectionID, regionID, actor, "region.name.reset", func(region *asset.ConnectionRegion) (map[string]any, error) {
		old := region.NameOverride
		region.NameOverride = ""
		return map[string]any{"old_name_override": old, "new_name_override": ""}, nil
	})
}

func (s *Service) Retire(ctx context.Context, connectionID asset.ConnectionID, regionID, actor string) (asset.ConnectionRegion, error) {
	return s.transition(ctx, connectionID, regionID, actor, "region.retire", asset.RegionActive, asset.RegionRetired)
}

func (s *Service) Activate(ctx context.Context, connectionID asset.ConnectionID, regionID, actor string) (asset.ConnectionRegion, error) {
	return s.transition(ctx, connectionID, regionID, actor, "region.activate", asset.RegionRetired, asset.RegionActive)
}

func (s *Service) Exclude(ctx context.Context, connectionID asset.ConnectionID, regionID, actor string) (asset.ConnectionRegion, error) {
	return s.mutate(ctx, connectionID, regionID, actor, "region.exclude", func(region *asset.ConnectionRegion) (map[string]any, error) {
		if region.Lifecycle == asset.RegionExcluded {
			return nil, ErrRegionInactive
		}
		old := region.Lifecycle
		region.Lifecycle = asset.RegionExcluded
		return lifecycleEvidence(old, region.Lifecycle), nil
	})
}

func (s *Service) Restore(ctx context.Context, connectionID asset.ConnectionID, regionID, actor string) (asset.ConnectionRegion, error) {
	return s.transition(ctx, connectionID, regionID, actor, "region.restore", asset.RegionExcluded, asset.RegionActive)
}

func (s *Service) Summary(ctx context.Context, connectionID asset.ConnectionID) (Summary, error) {
	if err := s.ensureConnection(ctx, s.repositories, connectionID); err != nil {
		return Summary{}, err
	}
	counts, err := s.repositories.Regions().CountRegionsByLifecycle(ctx, connectionID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{Active: counts[asset.RegionActive], Retired: counts[asset.RegionRetired], Excluded: counts[asset.RegionExcluded]}, nil
}

func (s *Service) List(ctx context.Context, options persistence.RegionListOptions) (persistence.Page[asset.ConnectionRegion], error) {
	return s.repositories.Regions().ListActiveConnectionRegions(ctx, options)
}

func (s *Service) transition(ctx context.Context, connectionID asset.ConnectionID, regionID, actor, action string, from, to asset.RegionLifecycle) (asset.ConnectionRegion, error) {
	return s.mutate(ctx, connectionID, regionID, actor, action, func(region *asset.ConnectionRegion) (map[string]any, error) {
		if region.Lifecycle != from {
			return nil, ErrRegionInactive
		}
		old := region.Lifecycle
		region.Lifecycle = to
		return lifecycleEvidence(old, to), nil
	})
}

func (s *Service) mutate(ctx context.Context, connectionID asset.ConnectionID, regionID, actor, action string, change func(*asset.ConnectionRegion) (map[string]any, error)) (asset.ConnectionRegion, error) {
	regionID = strings.TrimSpace(regionID)
	if regionID == "" {
		return asset.ConnectionRegion{}, ErrRegionIDEmpty
	}
	var updated asset.ConnectionRegion
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := s.ensureConnection(ctx, repositories, connectionID); err != nil {
			return err
		}
		region, err := findRegion(ctx, repositories, connectionID, regionID)
		if err != nil {
			return err
		}
		expectedRevision := region.Revision
		evidence, err := change(&region)
		if err != nil {
			return err
		}
		region.UpdatedAt = s.now()
		if err := repositories.Regions().PutRegionIfUnchanged(ctx, region, expectedRevision); err != nil {
			return mapConflict(err)
		}
		region.Revision = expectedRevision + 1
		if err := s.appendManualAudit(ctx, repositories, region, actor, action, evidence); err != nil {
			return err
		}
		updated = region
		return nil
	})
	return updated, err
}

func (s *Service) appendManualAudit(ctx context.Context, repositories persistence.Repositories, region asset.ConnectionRegion, actor, action string, evidence map[string]any) error {
	return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
		ID: execution.AuditEventID(s.entityID("aud")), ConnectionID: region.ConnectionID, Actor: normalizedActor(actor), Action: action,
		TargetType: "connection_region", TargetID: region.RegionID, Result: "updated", Evidence: evidence, CreatedAt: s.now(),
	})
}

func (s *Service) ensureConnection(ctx context.Context, repositories persistence.Repositories, connectionID asset.ConnectionID) error {
	if connectionID == "" {
		return persistence.ErrNotFound
	}
	connection, err := repositories.Connections().GetConnection(ctx, connectionID)
	if err != nil {
		return err
	}
	if connection.Status == asset.ConnectionDeleted {
		return persistence.ErrNotFound
	}
	if connection.Status != asset.ConnectionActive {
		return asset.ErrConnectionNotValidated
	}
	return nil
}

func findRegion(ctx context.Context, repositories persistence.Repositories, connectionID asset.ConnectionID, regionID string) (asset.ConnectionRegion, error) {
	regions, err := repositories.Regions().ListRegionsByConnection(ctx, connectionID)
	if err != nil {
		return asset.ConnectionRegion{}, err
	}
	for _, region := range regions {
		if region.RegionID == regionID {
			return region, nil
		}
	}
	return asset.ConnectionRegion{}, ErrRegionNotFound
}

func normalizeDiscovery(discovered []contracts.DiscoveredRegion) ([]contracts.DiscoveredRegion, error) {
	if len(discovered) == 0 {
		return nil, ErrInvalidDiscovery
	}
	seen := make(map[string]struct{}, len(discovered))
	result := make([]contracts.DiscoveredRegion, 0, len(discovered))
	for _, region := range discovered {
		region.RegionID = strings.TrimSpace(region.RegionID)
		region.Name = strings.TrimSpace(region.Name)
		if region.RegionID == "" {
			return nil, ErrInvalidDiscovery
		}
		if _, exists := seen[region.RegionID]; exists {
			return nil, fmt.Errorf("%w: duplicate Region ID %q", ErrInvalidDiscovery, region.RegionID)
		}
		seen[region.RegionID] = struct{}{}
		result = append(result, region)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RegionID < result[j].RegionID })
	return result, nil
}

func shouldRetireOnRefresh(region contracts.DiscoveredRegion) bool {
	return region.RegionID == "rus-west-1" || strings.Contains(region.Name, "关停")
}

func regionMap(regions []asset.ConnectionRegion) map[string]asset.ConnectionRegion {
	result := make(map[string]asset.ConnectionRegion, len(regions))
	for _, region := range regions {
		result[region.RegionID] = region
	}
	return result
}

func (s *Summary) addLifecycleCounts(regions map[string]asset.ConnectionRegion) {
	for _, region := range regions {
		switch region.Lifecycle {
		case asset.RegionActive:
			s.Active++
		case asset.RegionRetired:
			s.Retired++
		case asset.RegionExcluded:
			s.Excluded++
		}
	}
}

func summaryEvidence(summary Summary) map[string]any {
	return map[string]any{
		"added": summary.Added, "updated": summary.Updated, "missing": summary.Missing,
		"active": summary.Active, "retired": summary.Retired, "excluded": summary.Excluded,
	}
}

func lifecycleEvidence(old, next asset.RegionLifecycle) map[string]any {
	return map[string]any{"old_lifecycle": old, "new_lifecycle": next}
}

func normalizedActor(actor string) string {
	if actor = strings.TrimSpace(actor); actor != "" {
		return actor
	}
	return "system"
}

func mapConflict(err error) error {
	if errors.Is(err, persistence.ErrConflict) {
		return fmt.Errorf("%w: %v", ErrRegionConflict, err)
	}
	return err
}
