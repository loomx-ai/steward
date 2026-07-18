package finding

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type ID string

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type Status string

const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

type Finding struct {
	ID                 ID             `json:"id"`
	AssetID            asset.AssetID  `json:"asset_id"`
	RuleID             string         `json:"rule_id"`
	Status             Status         `json:"status"`
	Severity           Severity       `json:"severity"`
	Title              string         `json:"title"`
	Description        string         `json:"description"`
	Evidence           map[string]any `json:"evidence"`
	SpecBundleRevision string         `json:"spec_bundle_revision"`
	FirstSeenAt        time.Time      `json:"first_seen_at"`
	LastSeenAt         time.Time      `json:"last_seen_at"`
	ClosedAt           *time.Time     `json:"closed_at,omitempty"`
}
