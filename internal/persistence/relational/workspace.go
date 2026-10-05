package relational

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/loomx-ai/steward/internal/core/workspace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNoWorkspace reports tenant data reached without a workspace in context.
var ErrNoWorkspace = errors.New("repository access has no workspace")

// tenantTables hold rows of exactly one workspace each. resource_kinds (the
// provider catalog) and SQLite's search side tables stay global.
var tenantTables = map[string]bool{
	"action_attempts": true, "asset_changes": true, "asset_observations": true, "assets": true,
	"audit_events": true, "cleanup_task_rows": true, "cleanup_tasks": true, "cloud_connections": true,
	"connection_credentials": true, "connection_regions": true, "execution_attempts": true, "findings": true,
	"graph_revisions": true, "inventory_versions": true, "job_logs": true, "jobs": true, "lifecycle_bindings": true,
	"outbox_events": true, "relationships": true, "scan_schedule_runs": true, "scan_schedules": true,
	"scan_shards": true, "scan_tasks": true, "scopes": true, "workspace_settings": true,
}

// workspaceScope is a GORM plugin. Every statement whose root table is a
// tenant table is confined to the context's workspace: reads, updates and
// deletes get a workspace_id predicate (subqueries included, GORM renders them
// through the query callbacks) and inserts get the column. Joined tables are
// reached through random, globally unique IDs of an already confined row.
type workspaceScope struct {
	// strict refuses statements without a workspace; otherwise they use
	// workspace.Default, which is all a self-hosted server has.
	strict atomic.Bool
}

const workspaceScopeName = "steward:workspace"

func (*workspaceScope) Name() string { return workspaceScopeName }

func (p *workspaceScope) Initialize(db *gorm.DB) error {
	callbacks := db.Callback()
	if err := callbacks.Query().Before("gorm:query").Register(workspaceScopeName, p.confine); err != nil {
		return err
	}
	if err := callbacks.Row().Before("gorm:row").Register(workspaceScopeName, p.confine); err != nil {
		return err
	}
	if err := callbacks.Update().Before("gorm:update").Register(workspaceScopeName, p.confine); err != nil {
		return err
	}
	if err := callbacks.Delete().Before("gorm:delete").Register(workspaceScopeName, p.confine); err != nil {
		return err
	}
	return callbacks.Create().Before("gorm:create").Register(workspaceScopeName, p.stamp)
}

func workspacePlugin(db *gorm.DB) *workspaceScope {
	if plugin, ok := db.Config.Plugins[workspaceScopeName].(*workspaceScope); ok {
		return plugin
	}
	plugin := &workspaceScope{}
	if err := db.Use(plugin); err != nil {
		panic(err)
	}
	return plugin
}

// resolve returns the workspace of ctx; all is true for AcrossAll reads.
func (p *workspaceScope) resolve(ctx context.Context) (id workspace.ID, all bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if workspace.IsAcrossAll(ctx) {
		if !p.strict.Load() {
			// A self-hosted server has nothing beyond the default workspace.
			return workspace.Default, false, nil
		}
		return "", true, nil
	}
	if id, ok := workspace.From(ctx); ok {
		return id, false, nil
	}
	if p.strict.Load() {
		return "", false, ErrNoWorkspace
	}
	return workspace.Default, false, nil
}

// rootTable splits "scan_tasks AS tasks" into the table and the name its
// columns are qualified with.
func rootTable(stmt *gorm.Statement) (table, alias string) {
	table = strings.TrimSpace(stmt.Table)
	if table == "" && stmt.TableExpr != nil {
		table = strings.TrimSpace(stmt.TableExpr.SQL)
	}
	fields := strings.Fields(table)
	switch {
	case len(fields) == 3 && strings.EqualFold(fields[1], "as"):
		return fields[0], fields[2]
	case len(fields) == 2:
		return fields[0], fields[1]
	case len(fields) == 1:
		return fields[0], fields[0]
	}
	return "", ""
}

func (p *workspaceScope) confine(db *gorm.DB) {
	table, alias := rootTable(db.Statement)
	if !tenantTables[table] {
		return
	}
	id, all, err := p.resolve(db.Statement.Context)
	if all {
		return
	}
	if err != nil {
		// Subquery errors do not reach the outer statement, so the predicate
		// itself must fail closed.
		_ = db.AddError(err)
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
		return
	}
	db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Eq{Column: clause.Column{Table: alias, Name: "workspace_id"}, Value: string(id)},
	}})
}

func (p *workspaceScope) stamp(db *gorm.DB) {
	table, _ := rootTable(db.Statement)
	if !tenantTables[table] {
		return
	}
	id, all, err := p.resolve(db.Statement.Context)
	if err == nil && all {
		err = errors.New("repository insert must name one workspace")
	}
	if err != nil {
		_ = db.AddError(err)
		return
	}
	// Row structs of tenant tables carry a WorkspaceID field for this.
	db.Statement.SetColumn("workspace_id", string(id), true)
	// An upsert must never update another workspace's row that happens to
	// share its conflict key.
	if existing, ok := db.Statement.Clauses["ON CONFLICT"]; ok {
		if onConflict, ok := existing.Expression.(clause.OnConflict); ok && !onConflict.DoNothing {
			onConflict.Where.Exprs = append(onConflict.Where.Exprs, clause.Expr{SQL: table + ".workspace_id = excluded.workspace_id"})
			db.Statement.AddClause(onConflict)
		}
	}
}

// workspaceArgument is the workspace of ctx for hand-written SQL, which the
// plugin cannot rewrite. Without one it is "", which matches no row.
func (s *Store) workspaceArgument(ctx context.Context) (string, error) {
	id, all, err := s.workspaces.resolve(ctx)
	if err == nil && all {
		err = errors.New("hand-written repository SQL must name one workspace")
	}
	return string(id), err
}
