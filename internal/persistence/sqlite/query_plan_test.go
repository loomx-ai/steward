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
		plan      string // a substring of the expected plan
		sorted    bool   // ORDER BY must come from the index, not a temp B-tree
	}{
		// A keyword search reads the trigram index, not every asset.
		{`SELECT assets.id FROM assets WHERE connection_id = 'connection' AND closed_at IS NULL AND assets.id IN (
			SELECT asset_search_rows.asset_id FROM asset_search
			JOIN asset_search_rows ON asset_search_rows.id = asset_search.rowid
			WHERE asset_search.document GLOB '*10.0.1*')`,
			"VIRTUAL TABLE INDEX 0:G", false},
		{`DELETE FROM asset_observations WHERE scan_task_id = 'scan' AND EXISTS (
			SELECT 1 FROM assets WHERE assets.id = asset_observations.asset_id AND assets.last_seen_at > asset_observations.observed_at)`,
			"USING INDEX idx_asset_observations_scan_task ", false},
		{`SELECT * FROM job_logs WHERE aggregate_type = 'scan_task' AND aggregate_id = 'scan'
			ORDER BY created_at ASC, id ASC LIMIT 50`, "USING INDEX idx_job_logs_aggregate_cursor ", true},
		{`SELECT * FROM job_logs WHERE aggregate_type = 'scan_task' AND aggregate_id = 'scan'
			AND (created_at < 1 OR (created_at = 1 AND id < 'log')) ORDER BY created_at DESC, id DESC LIMIT 50`,
			"USING INDEX idx_job_logs_aggregate_cursor ", true},
		{`SELECT * FROM asset_changes WHERE scan_task_id = 'scan' ORDER BY changed_at DESC, id DESC LIMIT 51`,
			"USING INDEX idx_asset_changes_scan_cursor ", true},
		{`SELECT * FROM assets WHERE closed_at IS NULL AND connection_id = 'connection' AND scope_id IN ('a', 'b') ORDER BY id ASC`,
			"USING INDEX idx_assets_connection_scope ", false},
		{`SELECT * FROM findings WHERE (last_seen_at > 1 OR (last_seen_at = 1 AND id > 'f')) ORDER BY last_seen_at ASC, id ASC LIMIT 51`,
			"USING INDEX idx_findings_cursor ", true},
		{`SELECT * FROM assets WHERE closed_at IS NULL ORDER BY first_seen_at ASC, id ASC LIMIT 51`,
			"USING INDEX idx_assets_list_cursor ", true},
		{`SELECT COUNT(*), SUM(revision) FROM findings WHERE asset_id IN (SELECT id FROM assets WHERE connection_id = 'connection')`,
			"USING COVERING INDEX idx_findings_asset_revision ", false},
		{`SELECT DISTINCT observations.asset_id FROM scan_shards AS shards
			JOIN scan_tasks AS tasks ON tasks.id = shards.scan_task_id
			JOIN asset_observations AS observations ON observations.scan_shard_id = shards.id
			WHERE shards.scope_id = 'scope' AND shards.target_key = 'vpc' AND shards.source = 'source' AND shards.resource_kind_id = '' AND tasks.connection_id = 'connection'`,
			"USING INDEX idx_scan_shards_scope_target ", false},
		{`SELECT * FROM scopes WHERE connection_id = 'connection' AND parent_id = 'scope'`,
			"USING INDEX idx_scopes_connection_parent ", false},
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
		if !strings.Contains(joined, test.plan) || test.sorted && strings.Contains(joined, "TEMP B-TREE FOR ORDER BY") {
			t.Errorf("plan for %q:\n%s\nwant %q (sorted by index: %t)", test.statement, joined, test.plan, test.sorted)
		}
	}
}
