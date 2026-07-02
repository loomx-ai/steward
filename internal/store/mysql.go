package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prodesire/cloud-steward/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type MySQLStore struct {
	db *gorm.DB
}

func OpenMySQL(dsn string) (*gorm.DB, error) {
	return gorm.Open(mysql.Open(dsn), quietGormConfig())
}

func OpenSQLite(path string) (*gorm.DB, error) {
	return gorm.Open(sqlite.Open(path), quietGormConfig())
}

func NewSQLStore(db *gorm.DB) *MySQLStore {
	return NewMySQLStore(db)
}

func NewMySQLStore(db *gorm.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&mysqlAccount{},
		&mysqlScanJob{},
		&mysqlResource{},
		&mysqlResourceSnapshot{},
		&mysqlResourceEdge{},
		&mysqlCandidate{},
		&mysqlCleanupPlan{},
		&mysqlCleanupPlanItem{},
		&mysqlAuditEvent{},
	)
}

func (s *MySQLStore) SQLDB() (*sql.DB, error) {
	return s.db.DB()
}

func (s *MySQLStore) UpsertAccount(ctx context.Context, account domain.Account) (domain.Account, error) {
	now := time.Now().UTC()
	if account.ID == "" {
		var existing mysqlAccount
		err := s.db.WithContext(ctx).Where("provider = ? AND name = ?", account.Provider, account.Name).First(&existing).Error
		if err == nil {
			existing.AccessKeyID = account.AccessKeyID
			existing.AccessKeySecret = account.AccessKeySecret
			existing.UpdatedAt = now
			if err := s.db.WithContext(ctx).Save(&existing).Error; err != nil {
				return domain.Account{}, err
			}
			return existing.toDomain(), nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return domain.Account{}, err
		}
		account.ID = uuid.NewString()
		account.CreatedAt = now
	}
	account.UpdatedAt = now
	row := mysqlAccountFromDomain(account)
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&row).Error; err != nil {
		return domain.Account{}, err
	}
	return row.toDomain(), nil
}

func (s *MySQLStore) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	var row mysqlAccount
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return domain.Account{}, ErrNotFound
		}
		return domain.Account{}, err
	}
	return row.toDomain(), nil
}

func (s *MySQLStore) CreateScanJob(ctx context.Context, job domain.ScanJob) (domain.ScanJob, error) {
	if job.ID == "" {
		job.ID = uuid.NewString()
	}
	if job.Status == "" {
		job.Status = domain.ScanJobPending
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	row, err := mysqlScanJobFromDomain(job)
	if err != nil {
		return domain.ScanJob{}, err
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return domain.ScanJob{}, err
	}
	return row.toDomain()
}

func (s *MySQLStore) ClaimNextPendingScanJob(ctx context.Context) (domain.ScanJob, bool, error) {
	var claimed domain.ScanJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row mysqlScanJob
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("status = ?", domain.ScanJobPending).
			Order("created_at ASC").
			First(&row).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		row.Status = string(domain.ScanJobRunning)
		row.StartedAt = &now
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		converted, err := row.toDomain()
		if err != nil {
			return err
		}
		claimed = converted
		return nil
	})
	if err != nil {
		return domain.ScanJob{}, false, err
	}
	if claimed.ID == "" {
		return domain.ScanJob{}, false, nil
	}
	return claimed, true, nil
}

func (s *MySQLStore) MarkScanJobSucceeded(ctx context.Context, id string, resourceCount int) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&mysqlScanJob{}).Where("id = ?", id).Updates(map[string]any{
		"status":         string(domain.ScanJobSucceeded),
		"resource_count": resourceCount,
		"failure_reason": "",
		"finished_at":    &now,
	})
	return rowsErr(result)
}

func (s *MySQLStore) MarkScanJobFailed(ctx context.Context, id string, reason string) error {
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&mysqlScanJob{}).Where("id = ?", id).Updates(map[string]any{
		"status":         string(domain.ScanJobFailed),
		"failure_reason": reason,
		"finished_at":    &now,
	})
	return rowsErr(result)
}

func (s *MySQLStore) ListScanJobs(ctx context.Context) ([]domain.ScanJob, error) {
	var rows []mysqlScanJob
	if err := s.db.WithContext(ctx).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]domain.ScanJob, 0, len(rows))
	for _, row := range rows {
		job, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *MySQLStore) GetScanJob(ctx context.Context, id string) (domain.ScanJob, error) {
	var row mysqlScanJob
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return domain.ScanJob{}, ErrNotFound
		}
		return domain.ScanJob{}, err
	}
	return row.toDomain()
}

func (s *MySQLStore) UpsertResources(ctx context.Context, scanID string, resources []domain.Resource) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, resource := range resources {
			resource = resource.WithDerivedFields()
			if err := resource.Validate(); err != nil {
				return err
			}
			resource.ScanID = scanID
			var existing mysqlResource
			err := tx.Where(
				"provider = ? AND account_id = ? AND region = ? AND type = ? AND native_id = ?",
				resource.Provider, resource.AccountID, resource.Region, resource.Type, resource.NativeID,
			).First(&existing).Error
			if err != nil && err != gorm.ErrRecordNotFound {
				return err
			}
			if err == nil {
				resource.ID = existing.ID
			}
			if resource.ID == "" {
				resource.ID = uuid.NewString()
			}
			row, err := mysqlResourceFromDomain(resource)
			if err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				UpdateAll: true,
			}).Create(&row).Error; err != nil {
				return err
			}
			snapshot := mysqlResourceSnapshot{
				ScanID:     scanID,
				ResourceID: row.ID,
				Raw:        row.Raw,
				CreatedAt:  time.Now().UTC(),
			}
			if err := tx.Create(&snapshot).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *MySQLStore) ListResources(ctx context.Context, filter domain.ResourceFilter) ([]domain.Resource, error) {
	query := s.db.WithContext(ctx).Model(&mysqlResource{})
	query = applyResourceFilter(query, filter)
	var rows []mysqlResource
	if err := query.Order("last_seen_at DESC, name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	resources := make([]domain.Resource, 0, len(rows))
	for _, row := range rows {
		resource, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func (s *MySQLStore) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	var row mysqlResource
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return domain.Resource{}, ErrNotFound
		}
		return domain.Resource{}, err
	}
	return row.toDomain()
}

func (s *MySQLStore) UpsertResourceEdges(ctx context.Context, scanID string, edges []domain.ResourceEdge) error {
	for _, edge := range edges {
		edge.ScanID = scanID
		if edge.ID == "" {
			var existing mysqlResourceEdge
			err := s.db.WithContext(ctx).
				Where("scan_id = ? AND source_resource_id = ? AND target_resource_id = ? AND type = ?", edge.ScanID, edge.SourceResourceID, edge.TargetResourceID, edge.Type).
				First(&existing).Error
			if err != nil && err != gorm.ErrRecordNotFound {
				return err
			}
			if err == nil {
				edge.ID = existing.ID
			} else {
				edge.ID = uuid.NewString()
			}
		}
		if edge.CreatedAt.IsZero() {
			edge.CreatedAt = time.Now().UTC()
		}
		row, err := mysqlResourceEdgeFromDomain(edge)
		if err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *MySQLStore) ListResourceEdges(ctx context.Context, filter domain.GraphFilter) ([]domain.ResourceEdge, error) {
	query := s.db.WithContext(ctx).Model(&mysqlResourceEdge{})
	if filter.ScanID != "" {
		query = query.Where("scan_id = ?", filter.ScanID)
	}
	if filter.ResourceID != "" {
		query = query.Where("source_resource_id = ? OR target_resource_id = ?", filter.ResourceID, filter.ResourceID)
	}
	var rows []mysqlResourceEdge
	if err := query.Order("source_resource_id ASC, target_resource_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	edges := make([]domain.ResourceEdge, 0, len(rows))
	for _, row := range rows {
		edge, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, nil
}

func (s *MySQLStore) UpsertCandidates(ctx context.Context, scanID string, candidates []domain.CleanupCandidate) error {
	for _, candidate := range candidates {
		candidate.ScanID = scanID
		if candidate.Status == "" {
			candidate.Status = domain.CandidateOpen
		}
		now := time.Now().UTC()
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = now
		}
		candidate.UpdatedAt = now
		if candidate.ID == "" {
			var existing mysqlCandidate
			err := s.db.WithContext(ctx).
				Where("scan_id = ? AND resource_id = ? AND rule_id = ?", candidate.ScanID, candidate.ResourceID, candidate.RuleID).
				First(&existing).Error
			if err != nil && err != gorm.ErrRecordNotFound {
				return err
			}
			if err == nil {
				candidate.ID = existing.ID
				candidate.Status = domain.CandidateStatus(existing.Status)
				candidate.CreatedAt = existing.CreatedAt
			} else {
				candidate.ID = uuid.NewString()
			}
		}
		row, err := mysqlCandidateFromDomain(candidate)
		if err != nil {
			return err
		}
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *MySQLStore) ListCandidates(ctx context.Context, filter domain.CandidateFilter) ([]domain.CleanupCandidate, error) {
	query := s.db.WithContext(ctx).Model(&mysqlCandidate{})
	if filter.ScanID != "" {
		query = query.Where("scan_id = ?", filter.ScanID)
	}
	if filter.RuleID != "" {
		query = query.Where("rule_id = ?", filter.RuleID)
	}
	if filter.Risk != "" {
		query = query.Where("risk = ?", filter.Risk)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var rows []mysqlCandidate
	if err := query.Order("estimated_monthly_savings DESC, created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.CleanupCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		resource, err := s.GetResource(ctx, candidate.ResourceID)
		if err == nil {
			candidate.Resource = resource
		}
		if !candidateMatchesResource(candidate, filter) {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (s *MySQLStore) GetCandidate(ctx context.Context, id string) (domain.CleanupCandidate, error) {
	var row mysqlCandidate
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return domain.CleanupCandidate{}, ErrNotFound
		}
		return domain.CleanupCandidate{}, err
	}
	candidate, err := row.toDomain()
	if err != nil {
		return domain.CleanupCandidate{}, err
	}
	resource, err := s.GetResource(ctx, candidate.ResourceID)
	if err == nil {
		candidate.Resource = resource
	}
	return candidate, nil
}

func (s *MySQLStore) UpdateCandidateStatus(ctx context.Context, id string, status domain.CandidateStatus) error {
	result := s.db.WithContext(ctx).Model(&mysqlCandidate{}).Where("id = ?", id).Updates(map[string]any{
		"status":     string(status),
		"updated_at": time.Now().UTC(),
	})
	return rowsErr(result)
}

func (s *MySQLStore) CreateCleanupPlan(ctx context.Context, plan domain.CleanupPlan, items []domain.CleanupPlanItem) (domain.CleanupPlan, error) {
	if plan.ID == "" {
		plan.ID = uuid.NewString()
	}
	if plan.Status == "" {
		plan.Status = domain.PlanStatusDraft
	}
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = time.Now().UTC()
	}
	plan.ResourceCount = len(items)
	row := mysqlCleanupPlanFromDomain(plan)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		for i, item := range items {
			if item.ID == "" {
				item.ID = uuid.NewString()
			}
			item.PlanID = plan.ID
			if item.Order == 0 {
				item.Order = i + 1
			}
			if item.CreatedAt.IsZero() {
				item.CreatedAt = plan.CreatedAt
			}
			itemRow, err := mysqlCleanupPlanItemFromDomain(item)
			if err != nil {
				return err
			}
			if err := tx.Create(&itemRow).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.CleanupPlan{}, err
	}
	return row.toDomain(), nil
}

func (s *MySQLStore) ListCleanupPlans(ctx context.Context) ([]domain.CleanupPlan, error) {
	var rows []mysqlCleanupPlan
	if err := s.db.WithContext(ctx).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	plans := make([]domain.CleanupPlan, 0, len(rows))
	for _, row := range rows {
		plans = append(plans, row.toDomain())
	}
	return plans, nil
}

func (s *MySQLStore) GetCleanupPlan(ctx context.Context, id string) (domain.CleanupPlan, error) {
	var row mysqlCleanupPlan
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return domain.CleanupPlan{}, ErrNotFound
		}
		return domain.CleanupPlan{}, err
	}
	return row.toDomain(), nil
}

func (s *MySQLStore) ListPlanItems(ctx context.Context, planID string) ([]domain.CleanupPlanItem, error) {
	var rows []mysqlCleanupPlanItem
	if err := s.db.WithContext(ctx).Where("plan_id = ?", planID).Order("item_order ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if _, err := s.GetCleanupPlan(ctx, planID); err != nil {
			return nil, err
		}
	}
	items := make([]domain.CleanupPlanItem, 0, len(rows))
	for _, row := range rows {
		item, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		resource, err := s.GetResource(ctx, item.ResourceID)
		if err == nil {
			item.Resource = resource
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *MySQLStore) UpdateCleanupPlan(ctx context.Context, plan domain.CleanupPlan) error {
	row := mysqlCleanupPlanFromDomain(plan)
	result := s.db.WithContext(ctx).Save(&row)
	return rowsErr(result)
}

func (s *MySQLStore) UpdatePlanItemResult(ctx context.Context, itemID string, result string, requestID string) error {
	update := s.db.WithContext(ctx).Model(&mysqlCleanupPlanItem{}).Where("id = ?", itemID).Updates(map[string]any{
		"result":     result,
		"request_id": requestID,
	})
	return rowsErr(update)
}

func (s *MySQLStore) CreateAuditEvent(ctx context.Context, event domain.AuditEvent) (domain.AuditEvent, error) {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	row := mysqlAuditEventFromDomain(event)
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return domain.AuditEvent{}, err
	}
	return row.toDomain(), nil
}

func (s *MySQLStore) ListAuditEvents(ctx context.Context, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	query := s.db.WithContext(ctx).Model(&mysqlAuditEvent{})
	if filter.Action != "" {
		query = query.Where("action = ?", filter.Action)
	}
	if filter.TargetType != "" {
		query = query.Where("target_type = ?", filter.TargetType)
	}
	if filter.TargetID != "" {
		query = query.Where("target_id = ?", filter.TargetID)
	}
	var rows []mysqlAuditEvent
	if err := query.Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	events := make([]domain.AuditEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, row.toDomain())
	}
	return events, nil
}

func (s *MySQLStore) GetSavingsReport(ctx context.Context) (domain.SavingsReport, error) {
	candidates, err := s.ListCandidates(ctx, domain.CandidateFilter{})
	if err != nil {
		return domain.SavingsReport{}, err
	}
	plans, err := s.ListCleanupPlans(ctx)
	if err != nil {
		return domain.SavingsReport{}, err
	}
	report := domain.SavingsReport{
		CandidateCount:         len(candidates),
		PlanCount:              len(plans),
		EstimatedSavingsByTeam: map[string]float64{},
		EstimatedSavingsByType: map[string]float64{},
	}
	for _, candidate := range candidates {
		report.EstimatedMonthlySavings += candidate.EstimatedMonthlySavings
		team := candidate.Resource.Ownership.Team
		if team == "" {
			team = "unassigned"
		}
		report.EstimatedSavingsByTeam[team] += candidate.EstimatedMonthlySavings
		report.EstimatedSavingsByType[string(candidate.Resource.Type)] += candidate.EstimatedMonthlySavings
	}
	for _, plan := range plans {
		if plan.Status == domain.PlanStatusCompleted {
			report.CompletedPlanCount++
		}
	}
	return report, nil
}

type mysqlAccount struct {
	ID              string `gorm:"primaryKey"`
	Name            string
	Provider        string
	AccessKeyID     string
	AccessKeySecret string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (mysqlAccount) TableName() string { return "accounts" }

type mysqlScanJob struct {
	ID            string `gorm:"primaryKey"`
	AccountID     string
	AccountName   string
	Provider      string
	Mode          string
	Regions       datatypes.JSON
	Status        string
	ResourceCount int
	FailureReason string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

func (mysqlScanJob) TableName() string { return "scan_jobs" }

type mysqlResource struct {
	ID          string `gorm:"primaryKey"`
	ScanID      string
	Provider    string
	AccountID   string
	Region      string
	Type        string
	NativeID    string
	Name        string
	State       string
	Tags        datatypes.JSON
	Owner       string
	Team        string
	Application string
	Environment string
	CostCenter  string
	Protected   bool
	CreatedAt   time.Time
	LastSeenAt  time.Time
	Raw         datatypes.JSON
}

func (mysqlResource) TableName() string { return "resources" }

type mysqlResourceSnapshot struct {
	ID         uint64 `gorm:"primaryKey"`
	ScanID     string
	ResourceID string
	Raw        datatypes.JSON
	CreatedAt  time.Time
}

func (mysqlResourceSnapshot) TableName() string { return "resource_snapshots" }

type mysqlResourceEdge struct {
	ID               string `gorm:"primaryKey"`
	ScanID           string `gorm:"index"`
	SourceResourceID string `gorm:"index"`
	TargetResourceID string `gorm:"index"`
	Type             string
	Source           string
	Confidence       float64
	Evidence         datatypes.JSON
	CreatedAt        time.Time
}

func (mysqlResourceEdge) TableName() string { return "resource_edges" }

type mysqlCandidate struct {
	ID                      string `gorm:"primaryKey"`
	ScanID                  string `gorm:"index"`
	ResourceID              string `gorm:"index"`
	RuleID                  string `gorm:"index"`
	Reason                  string
	Evidence                datatypes.JSON
	Confidence              float64
	Risk                    string
	RecommendedAction       string
	EstimatedMonthlySavings float64
	Status                  string `gorm:"index"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (mysqlCandidate) TableName() string { return "cleanup_candidates" }

type mysqlCleanupPlan struct {
	ID                      string `gorm:"primaryKey"`
	Status                  string `gorm:"index"`
	DryRun                  bool
	ResourceCount           int
	Risk                    string
	EstimatedMonthlySavings float64
	CreatedBy               string
	ApprovedBy              string
	ApprovalComment         string
	CreatedAt               time.Time
	ApprovedAt              *time.Time
	ExecutedAt              *time.Time
}

func (mysqlCleanupPlan) TableName() string { return "cleanup_plans" }

type mysqlCleanupPlanItem struct {
	ID                      string `gorm:"primaryKey"`
	PlanID                  string `gorm:"index"`
	CandidateID             string `gorm:"index"`
	ResourceID              string `gorm:"index"`
	Action                  string
	ItemOrder               int
	Risk                    string
	Blocked                 bool
	BlockReason             string
	Reason                  string
	Evidence                datatypes.JSON
	EstimatedMonthlySavings float64
	Result                  string
	RequestID               string
	CreatedAt               time.Time
}

func (mysqlCleanupPlanItem) TableName() string { return "cleanup_plan_items" }

type mysqlAuditEvent struct {
	ID         string `gorm:"primaryKey"`
	Actor      string
	Action     string `gorm:"index"`
	TargetType string `gorm:"index"`
	TargetID   string `gorm:"index"`
	Result     string
	Message    string
	RequestID  string
	CreatedAt  time.Time
}

func (mysqlAuditEvent) TableName() string { return "audit_events" }

func mysqlAccountFromDomain(account domain.Account) mysqlAccount {
	return mysqlAccount{
		ID:              account.ID,
		Name:            account.Name,
		Provider:        string(account.Provider),
		AccessKeyID:     account.AccessKeyID,
		AccessKeySecret: account.AccessKeySecret,
		CreatedAt:       account.CreatedAt,
		UpdatedAt:       account.UpdatedAt,
	}
}

func (m mysqlAccount) toDomain() domain.Account {
	return domain.Account{
		ID:              m.ID,
		Name:            m.Name,
		Provider:        domain.Provider(m.Provider),
		AccessKeyID:     m.AccessKeyID,
		AccessKeySecret: m.AccessKeySecret,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func mysqlScanJobFromDomain(job domain.ScanJob) (mysqlScanJob, error) {
	regions, err := json.Marshal(job.Regions)
	if err != nil {
		return mysqlScanJob{}, err
	}
	return mysqlScanJob{
		ID:            job.ID,
		AccountID:     job.AccountID,
		AccountName:   job.AccountName,
		Provider:      string(job.Provider),
		Mode:          string(job.Mode),
		Regions:       datatypes.JSON(regions),
		Status:        string(job.Status),
		ResourceCount: job.ResourceCount,
		FailureReason: job.FailureReason,
		CreatedAt:     job.CreatedAt,
		StartedAt:     job.StartedAt,
		FinishedAt:    job.FinishedAt,
	}, nil
}

func (m mysqlScanJob) toDomain() (domain.ScanJob, error) {
	var regions []string
	if len(m.Regions) > 0 {
		if err := json.Unmarshal(m.Regions, &regions); err != nil {
			return domain.ScanJob{}, err
		}
	}
	return domain.ScanJob{
		ID:            m.ID,
		AccountID:     m.AccountID,
		AccountName:   m.AccountName,
		Provider:      domain.Provider(m.Provider),
		Mode:          domain.ScanMode(m.Mode),
		Regions:       regions,
		Status:        domain.ScanJobStatus(m.Status),
		ResourceCount: m.ResourceCount,
		FailureReason: m.FailureReason,
		CreatedAt:     m.CreatedAt,
		StartedAt:     m.StartedAt,
		FinishedAt:    m.FinishedAt,
	}, nil
}

func mysqlResourceFromDomain(resource domain.Resource) (mysqlResource, error) {
	tags, err := json.Marshal(resource.Tags)
	if err != nil {
		return mysqlResource{}, err
	}
	raw, err := json.Marshal(resource.Raw)
	if err != nil {
		return mysqlResource{}, err
	}
	return mysqlResource{
		ID:          resource.ID,
		ScanID:      resource.ScanID,
		Provider:    string(resource.Provider),
		AccountID:   resource.AccountID,
		Region:      resource.Region,
		Type:        string(resource.Type),
		NativeID:    resource.NativeID,
		Name:        resource.Name,
		State:       resource.State,
		Tags:        datatypes.JSON(tags),
		Owner:       resource.Ownership.Owner,
		Team:        resource.Ownership.Team,
		Application: resource.Ownership.Application,
		Environment: resource.Ownership.Environment,
		CostCenter:  resource.Ownership.CostCenter,
		Protected:   resource.Protected,
		CreatedAt:   resource.CreatedAt,
		LastSeenAt:  resource.LastSeenAt,
		Raw:         datatypes.JSON(raw),
	}, nil
}

func (m mysqlResource) toDomain() (domain.Resource, error) {
	var tags map[string]string
	if len(m.Tags) > 0 {
		if err := json.Unmarshal(m.Tags, &tags); err != nil {
			return domain.Resource{}, err
		}
	}
	var raw map[string]any
	if len(m.Raw) > 0 {
		if err := json.Unmarshal(m.Raw, &raw); err != nil {
			return domain.Resource{}, err
		}
	}
	return domain.Resource{
		ID:        m.ID,
		ScanID:    m.ScanID,
		Provider:  domain.Provider(m.Provider),
		AccountID: m.AccountID,
		Region:    m.Region,
		Type:      domain.ResourceType(m.Type),
		NativeID:  m.NativeID,
		Name:      m.Name,
		State:     m.State,
		Tags:      tags,
		Ownership: domain.Ownership{
			Owner:       m.Owner,
			Team:        m.Team,
			Application: m.Application,
			Environment: m.Environment,
			CostCenter:  m.CostCenter,
		},
		Protected:  m.Protected,
		CreatedAt:  m.CreatedAt,
		LastSeenAt: m.LastSeenAt,
		Raw:        raw,
	}, nil
}

func mysqlResourceEdgeFromDomain(edge domain.ResourceEdge) (mysqlResourceEdge, error) {
	evidence, err := json.Marshal(edge.Evidence)
	if err != nil {
		return mysqlResourceEdge{}, err
	}
	return mysqlResourceEdge{
		ID:               edge.ID,
		ScanID:           edge.ScanID,
		SourceResourceID: edge.SourceResourceID,
		TargetResourceID: edge.TargetResourceID,
		Type:             edge.Type,
		Source:           edge.Source,
		Confidence:       edge.Confidence,
		Evidence:         datatypes.JSON(evidence),
		CreatedAt:        edge.CreatedAt,
	}, nil
}

func (m mysqlResourceEdge) toDomain() (domain.ResourceEdge, error) {
	var evidence map[string]any
	if len(m.Evidence) > 0 {
		if err := json.Unmarshal(m.Evidence, &evidence); err != nil {
			return domain.ResourceEdge{}, err
		}
	}
	return domain.ResourceEdge{
		ID:               m.ID,
		ScanID:           m.ScanID,
		SourceResourceID: m.SourceResourceID,
		TargetResourceID: m.TargetResourceID,
		Type:             m.Type,
		Source:           m.Source,
		Confidence:       m.Confidence,
		Evidence:         evidence,
		CreatedAt:        m.CreatedAt,
	}, nil
}

func mysqlCandidateFromDomain(candidate domain.CleanupCandidate) (mysqlCandidate, error) {
	evidence, err := json.Marshal(candidate.Evidence)
	if err != nil {
		return mysqlCandidate{}, err
	}
	return mysqlCandidate{
		ID:                      candidate.ID,
		ScanID:                  candidate.ScanID,
		ResourceID:              candidate.ResourceID,
		RuleID:                  candidate.RuleID,
		Reason:                  candidate.Reason,
		Evidence:                datatypes.JSON(evidence),
		Confidence:              candidate.Confidence,
		Risk:                    string(candidate.Risk),
		RecommendedAction:       candidate.RecommendedAction,
		EstimatedMonthlySavings: candidate.EstimatedMonthlySavings,
		Status:                  string(candidate.Status),
		CreatedAt:               candidate.CreatedAt,
		UpdatedAt:               candidate.UpdatedAt,
	}, nil
}

func (m mysqlCandidate) toDomain() (domain.CleanupCandidate, error) {
	var evidence map[string]any
	if len(m.Evidence) > 0 {
		if err := json.Unmarshal(m.Evidence, &evidence); err != nil {
			return domain.CleanupCandidate{}, err
		}
	}
	return domain.CleanupCandidate{
		ID:                      m.ID,
		ScanID:                  m.ScanID,
		ResourceID:              m.ResourceID,
		RuleID:                  m.RuleID,
		Reason:                  m.Reason,
		Evidence:                evidence,
		Confidence:              m.Confidence,
		Risk:                    domain.RiskLevel(m.Risk),
		RecommendedAction:       m.RecommendedAction,
		EstimatedMonthlySavings: m.EstimatedMonthlySavings,
		Status:                  domain.CandidateStatus(m.Status),
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
	}, nil
}

func mysqlCleanupPlanFromDomain(plan domain.CleanupPlan) mysqlCleanupPlan {
	return mysqlCleanupPlan{
		ID:                      plan.ID,
		Status:                  string(plan.Status),
		DryRun:                  plan.DryRun,
		ResourceCount:           plan.ResourceCount,
		Risk:                    string(plan.Risk),
		EstimatedMonthlySavings: plan.EstimatedMonthlySavings,
		CreatedBy:               plan.CreatedBy,
		ApprovedBy:              plan.ApprovedBy,
		ApprovalComment:         plan.ApprovalComment,
		CreatedAt:               plan.CreatedAt,
		ApprovedAt:              plan.ApprovedAt,
		ExecutedAt:              plan.ExecutedAt,
	}
}

func (m mysqlCleanupPlan) toDomain() domain.CleanupPlan {
	return domain.CleanupPlan{
		ID:                      m.ID,
		Status:                  domain.PlanStatus(m.Status),
		DryRun:                  m.DryRun,
		ResourceCount:           m.ResourceCount,
		Risk:                    domain.RiskLevel(m.Risk),
		EstimatedMonthlySavings: m.EstimatedMonthlySavings,
		CreatedBy:               m.CreatedBy,
		ApprovedBy:              m.ApprovedBy,
		ApprovalComment:         m.ApprovalComment,
		CreatedAt:               m.CreatedAt,
		ApprovedAt:              m.ApprovedAt,
		ExecutedAt:              m.ExecutedAt,
	}
}

func mysqlCleanupPlanItemFromDomain(item domain.CleanupPlanItem) (mysqlCleanupPlanItem, error) {
	evidence, err := json.Marshal(item.Evidence)
	if err != nil {
		return mysqlCleanupPlanItem{}, err
	}
	return mysqlCleanupPlanItem{
		ID:                      item.ID,
		PlanID:                  item.PlanID,
		CandidateID:             item.CandidateID,
		ResourceID:              item.ResourceID,
		Action:                  item.Action,
		ItemOrder:               item.Order,
		Risk:                    string(item.Risk),
		Blocked:                 item.Blocked,
		BlockReason:             item.BlockReason,
		Reason:                  item.Reason,
		Evidence:                datatypes.JSON(evidence),
		EstimatedMonthlySavings: item.EstimatedMonthlySavings,
		Result:                  item.Result,
		RequestID:               item.RequestID,
		CreatedAt:               item.CreatedAt,
	}, nil
}

func (m mysqlCleanupPlanItem) toDomain() (domain.CleanupPlanItem, error) {
	var evidence map[string]any
	if len(m.Evidence) > 0 {
		if err := json.Unmarshal(m.Evidence, &evidence); err != nil {
			return domain.CleanupPlanItem{}, err
		}
	}
	return domain.CleanupPlanItem{
		ID:                      m.ID,
		PlanID:                  m.PlanID,
		CandidateID:             m.CandidateID,
		ResourceID:              m.ResourceID,
		Action:                  m.Action,
		Order:                   m.ItemOrder,
		Risk:                    domain.RiskLevel(m.Risk),
		Blocked:                 m.Blocked,
		BlockReason:             m.BlockReason,
		Reason:                  m.Reason,
		Evidence:                evidence,
		EstimatedMonthlySavings: m.EstimatedMonthlySavings,
		Result:                  m.Result,
		RequestID:               m.RequestID,
		CreatedAt:               m.CreatedAt,
	}, nil
}

func mysqlAuditEventFromDomain(event domain.AuditEvent) mysqlAuditEvent {
	return mysqlAuditEvent{
		ID:         event.ID,
		Actor:      event.Actor,
		Action:     event.Action,
		TargetType: event.TargetType,
		TargetID:   event.TargetID,
		Result:     event.Result,
		Message:    event.Message,
		RequestID:  event.RequestID,
		CreatedAt:  event.CreatedAt,
	}
}

func (m mysqlAuditEvent) toDomain() domain.AuditEvent {
	return domain.AuditEvent{
		ID:         m.ID,
		Actor:      m.Actor,
		Action:     m.Action,
		TargetType: m.TargetType,
		TargetID:   m.TargetID,
		Result:     m.Result,
		Message:    m.Message,
		RequestID:  m.RequestID,
		CreatedAt:  m.CreatedAt,
	}
}

func applyResourceFilter(query *gorm.DB, filter domain.ResourceFilter) *gorm.DB {
	if filter.ScanID != "" {
		query = query.Where("scan_id = ?", filter.ScanID)
	}
	if filter.Provider != "" {
		query = query.Where("provider = ?", filter.Provider)
	}
	if filter.AccountID != "" {
		query = query.Where("account_id = ?", filter.AccountID)
	}
	if filter.Region != "" {
		query = query.Where("region = ?", filter.Region)
	}
	if filter.Type != "" {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.Query != "" {
		needle := "%" + strings.ToLower(strings.TrimSpace(filter.Query)) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(native_id) LIKE ? OR LOWER(type) LIKE ?", needle, needle, needle)
	}
	return query
}

func candidateMatchesResource(candidate domain.CleanupCandidate, filter domain.CandidateFilter) bool {
	if filter.ResourceType != "" && candidate.Resource.Type != filter.ResourceType {
		return false
	}
	if filter.Team != "" && candidate.Resource.Ownership.Team != filter.Team {
		return false
	}
	if filter.Region != "" && candidate.Resource.Region != filter.Region {
		return false
	}
	return true
}

func rowsErr(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func quietGormConfig() *gorm.Config {
	return &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}
}
