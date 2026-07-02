package domain

import (
	"errors"
	"strings"
	"time"
)

type Provider string

const (
	ProviderAliCloud Provider = "alicloud"
	ProviderDemo     Provider = "demo"
)

type ScanMode string

const (
	ScanModeDemo     ScanMode = "demo"
	ScanModeAliCloud ScanMode = "alicloud"
)

type ResourceType string

const (
	ResourceTypeECSInstance   ResourceType = "ecs_instance"
	ResourceTypeDisk          ResourceType = "disk"
	ResourceTypeEIP           ResourceType = "eip"
	ResourceTypeSecurityGroup ResourceType = "security_group"
	ResourceTypeSnapshot      ResourceType = "snapshot"
	ResourceTypeVPC           ResourceType = "vpc"
	ResourceTypeVSwitch       ResourceType = "vswitch"
)

type ScanJobStatus string

const (
	ScanJobPending   ScanJobStatus = "pending"
	ScanJobRunning   ScanJobStatus = "running"
	ScanJobSucceeded ScanJobStatus = "succeeded"
	ScanJobFailed    ScanJobStatus = "failed"
)

type CandidateStatus string

const (
	CandidateOpen     CandidateStatus = "open"
	CandidateAccepted CandidateStatus = "accepted"
	CandidateIgnored  CandidateStatus = "ignored"
	CandidateSnoozed  CandidateStatus = "snoozed"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

type PlanStatus string

const (
	PlanStatusDraft           PlanStatus = "draft"
	PlanStatusPendingApproval PlanStatus = "pending_approval"
	PlanStatusApproved        PlanStatus = "approved"
	PlanStatusRunning         PlanStatus = "running"
	PlanStatusCompleted       PlanStatus = "completed"
	PlanStatusFailed          PlanStatus = "failed"
	PlanStatusCanceled        PlanStatus = "canceled"
)

type Account struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Provider        Provider  `json:"provider"`
	AccessKeyID     string    `json:"-"`
	AccessKeySecret string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ScanJob struct {
	ID            string        `json:"id"`
	AccountID     string        `json:"account_id"`
	AccountName   string        `json:"account_name"`
	Provider      Provider      `json:"provider"`
	Mode          ScanMode      `json:"mode"`
	Regions       []string      `json:"regions"`
	Status        ScanJobStatus `json:"status"`
	ResourceCount int           `json:"resource_count"`
	FailureReason string        `json:"failure_reason,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	StartedAt     *time.Time    `json:"started_at,omitempty"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
}

type Ownership struct {
	Owner       string `json:"owner,omitempty"`
	Team        string `json:"team,omitempty"`
	Application string `json:"application,omitempty"`
	Environment string `json:"environment,omitempty"`
	CostCenter  string `json:"cost_center,omitempty"`
}

type Resource struct {
	ID         string            `json:"id"`
	ScanID     string            `json:"scan_id"`
	Provider   Provider          `json:"provider"`
	AccountID  string            `json:"account_id"`
	Region     string            `json:"region"`
	Type       ResourceType      `json:"type"`
	NativeID   string            `json:"native_id"`
	Name       string            `json:"name"`
	State      string            `json:"state"`
	Tags       map[string]string `json:"tags"`
	Ownership  Ownership         `json:"ownership"`
	Protected  bool              `json:"protected"`
	CreatedAt  time.Time         `json:"created_at"`
	LastSeenAt time.Time         `json:"last_seen_at"`
	Raw        map[string]any    `json:"raw"`
}

type ResourceFilter struct {
	ScanID    string
	Provider  Provider
	AccountID string
	Region    string
	Type      ResourceType
	Query     string
}

type ResourceEdge struct {
	ID               string         `json:"id"`
	ScanID           string         `json:"scan_id"`
	SourceResourceID string         `json:"source_resource_id"`
	TargetResourceID string         `json:"target_resource_id"`
	Type             string         `json:"type"`
	Source           string         `json:"source"`
	Confidence       float64        `json:"confidence"`
	Evidence         map[string]any `json:"evidence"`
	CreatedAt        time.Time      `json:"created_at"`
}

type GraphFilter struct {
	ScanID     string
	ResourceID string
	VPCID      string
}

type CleanupCandidate struct {
	ID                      string          `json:"id"`
	ScanID                  string          `json:"scan_id"`
	ResourceID              string          `json:"resource_id"`
	Resource                Resource        `json:"resource"`
	RuleID                  string          `json:"rule_id"`
	Reason                  string          `json:"reason"`
	Evidence                map[string]any  `json:"evidence"`
	Confidence              float64         `json:"confidence"`
	Risk                    RiskLevel       `json:"risk"`
	RecommendedAction       string          `json:"recommended_action"`
	EstimatedMonthlySavings float64         `json:"estimated_monthly_savings"`
	Status                  CandidateStatus `json:"status"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type CandidateFilter struct {
	ScanID       string
	RuleID       string
	ResourceType ResourceType
	Team         string
	Region       string
	Risk         RiskLevel
	Status       CandidateStatus
}

type CleanupPlan struct {
	ID                      string     `json:"id"`
	Status                  PlanStatus `json:"status"`
	DryRun                  bool       `json:"dry_run"`
	ResourceCount           int        `json:"resource_count"`
	Risk                    RiskLevel  `json:"risk"`
	EstimatedMonthlySavings float64    `json:"estimated_monthly_savings"`
	CreatedBy               string     `json:"created_by"`
	ApprovedBy              string     `json:"approved_by,omitempty"`
	ApprovalComment         string     `json:"approval_comment,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	ApprovedAt              *time.Time `json:"approved_at,omitempty"`
	ExecutedAt              *time.Time `json:"executed_at,omitempty"`
}

type CleanupPlanItem struct {
	ID                      string         `json:"id"`
	PlanID                  string         `json:"plan_id"`
	CandidateID             string         `json:"candidate_id"`
	ResourceID              string         `json:"resource_id"`
	Resource                Resource       `json:"resource"`
	Action                  string         `json:"action"`
	Order                   int            `json:"order"`
	Risk                    RiskLevel      `json:"risk"`
	Blocked                 bool           `json:"blocked"`
	BlockReason             string         `json:"block_reason,omitempty"`
	Reason                  string         `json:"reason"`
	Evidence                map[string]any `json:"evidence"`
	EstimatedMonthlySavings float64        `json:"estimated_monthly_savings"`
	Result                  string         `json:"result,omitempty"`
	RequestID               string         `json:"request_id,omitempty"`
	CreatedAt               time.Time      `json:"created_at"`
}

type AuditEvent struct {
	ID         string    `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Result     string    `json:"result"`
	Message    string    `json:"message"`
	RequestID  string    `json:"request_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type AuditFilter struct {
	Action     string
	TargetType string
	TargetID   string
}

type SavingsReport struct {
	CandidateCount          int                `json:"candidate_count"`
	PlanCount               int                `json:"plan_count"`
	EstimatedMonthlySavings float64            `json:"estimated_monthly_savings"`
	EstimatedSavingsByTeam  map[string]float64 `json:"estimated_savings_by_team"`
	EstimatedSavingsByType  map[string]float64 `json:"estimated_savings_by_type"`
	CompletedPlanCount      int                `json:"completed_plan_count"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type APIErrorResponse struct {
	Error APIError `json:"error"`
}

func ExtractOwnership(tags map[string]string) Ownership {
	return Ownership{
		Owner:       firstTag(tags, "owner", "cloud-steward:owner"),
		Team:        firstTag(tags, "team", "cloud-steward:team"),
		Application: firstTag(tags, "application", "app", "service", "cloud-steward:application"),
		Environment: firstTag(tags, "environment", "env", "stage", "cloud-steward:environment"),
		CostCenter:  firstTag(tags, "cost-center", "cost_center", "costcenter", "cloud-steward:cost-center"),
	}
}

func IsProtectedResource(tags map[string]string) bool {
	for key, value := range tags {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		normalizedValue := strings.ToLower(strings.TrimSpace(value))
		if normalizedKey == "cloud-steward:protect" && (normalizedValue == "true" || normalizedValue == "1" || normalizedValue == "yes") {
			return true
		}
		if (normalizedKey == "env" || normalizedKey == "environment" || normalizedKey == "cloud-steward:environment") &&
			(normalizedValue == "prod" || normalizedValue == "production") {
			return true
		}
	}
	return false
}

func (r Resource) Validate() error {
	if r.Provider == "" {
		return errors.New("provider is required")
	}
	if strings.TrimSpace(r.AccountID) == "" {
		return errors.New("account id is required")
	}
	if strings.TrimSpace(r.Region) == "" {
		return errors.New("region is required")
	}
	if r.Type == "" {
		return errors.New("resource type is required")
	}
	if strings.TrimSpace(r.NativeID) == "" {
		return errors.New("native id is required")
	}
	return nil
}

func (r Resource) WithDerivedFields() Resource {
	if r.Tags == nil {
		r.Tags = map[string]string{}
	}
	r.Ownership = ExtractOwnership(r.Tags)
	r.Protected = IsProtectedResource(r.Tags)
	if r.LastSeenAt.IsZero() {
		r.LastSeenAt = time.Now().UTC()
	}
	return r
}

func firstTag(tags map[string]string, keys ...string) string {
	if len(tags) == 0 {
		return ""
	}
	lowered := make(map[string]string, len(tags))
	for key, value := range tags {
		lowered[strings.ToLower(strings.TrimSpace(key))] = value
	}
	for _, key := range keys {
		if value, ok := lowered[strings.ToLower(key)]; ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
