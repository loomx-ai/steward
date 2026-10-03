package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/persistence"
	_ "github.com/mattn/go-sqlite3"
)

// Hot retention and pagination statements must stay index-driven: a full
// table scan or a temp B-tree sort here grows with the largest tables.
func TestHotStatementsUseIndexes(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := persistence.Migrate(db, "sqlite3", filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		statement string
		index     string
		sorted    bool // ORDER BY must come from the index, not a temp B-tree
	}{
		{`DELETE FROM asset_observations WHERE scan_task_id = 'scan' AND EXISTS (
			SELECT 1 FROM assets WHERE assets.id = asset_observations.asset_id AND assets.last_seen_at > asset_observations.observed_at)`,
			"idx_asset_observations_scan_task", false},
		{`SELECT * FROM job_logs WHERE aggregate_type = 'scan_task' AND aggregate_id = 'scan'
			ORDER BY created_at ASC, id ASC LIMIT 50`, "idx_job_logs_aggregate_cursor", true},
		{`SELECT * FROM job_logs WHERE aggregate_type = 'scan_task' AND aggregate_id = 'scan'
			AND (created_at < 1 OR (created_at = 1 AND id < 'log')) ORDER BY created_at DESC, id DESC LIMIT 50`,
			"idx_job_logs_aggregate_cursor", true},
		{`SELECT * FROM asset_changes WHERE scan_task_id = 'scan' ORDER BY changed_at DESC, id DESC LIMIT 51`,
			"idx_asset_changes_scan_cursor", true},
		{`SELECT * FROM assets WHERE closed_at IS NULL AND connection_id = 'connection' AND scope_id IN ('a', 'b') ORDER BY id ASC`,
			"idx_assets_connection_scope", false},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + test.statement)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, "USING INDEX "+test.index+" ") || test.sorted && strings.Contains(joined, "TEMP B-TREE FOR ORDER BY") {
			t.Errorf("plan for %q:\n%s\nwant index %s (sorted by index: %t)", test.statement, joined, test.index, test.sorted)
		}
	}
}
