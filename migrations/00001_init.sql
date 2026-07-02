-- +goose Up
CREATE TABLE IF NOT EXISTS accounts (
  id VARCHAR(64) PRIMARY KEY,
  name VARCHAR(255) NOT NULL,
  provider VARCHAR(64) NOT NULL,
  access_key_id VARCHAR(255) NOT NULL DEFAULT '',
  access_key_secret TEXT NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  UNIQUE KEY idx_accounts_provider_name (provider, name)
);

CREATE TABLE IF NOT EXISTS scan_jobs (
  id VARCHAR(64) PRIMARY KEY,
  account_id VARCHAR(64) NOT NULL,
  account_name VARCHAR(255) NOT NULL,
  provider VARCHAR(64) NOT NULL,
  mode VARCHAR(64) NOT NULL,
  regions JSON NOT NULL,
  status VARCHAR(64) NOT NULL,
  resource_count INT NOT NULL DEFAULT 0,
  failure_reason TEXT NOT NULL,
  created_at DATETIME(6) NOT NULL,
  started_at DATETIME(6) NULL,
  finished_at DATETIME(6) NULL,
  INDEX idx_scan_jobs_status_created_at (status, created_at),
  INDEX idx_scan_jobs_account_id (account_id),
  CONSTRAINT fk_scan_jobs_account FOREIGN KEY (account_id) REFERENCES accounts(id)
);

CREATE TABLE IF NOT EXISTS resources (
  id VARCHAR(64) PRIMARY KEY,
  scan_id VARCHAR(64) NOT NULL,
  provider VARCHAR(64) NOT NULL,
  account_id VARCHAR(64) NOT NULL,
  region VARCHAR(128) NOT NULL,
  type VARCHAR(128) NOT NULL,
  native_id VARCHAR(255) NOT NULL,
  name VARCHAR(255) NOT NULL,
  state VARCHAR(128) NOT NULL,
  tags JSON NOT NULL,
  owner VARCHAR(255) NOT NULL DEFAULT '',
  team VARCHAR(255) NOT NULL DEFAULT '',
  application VARCHAR(255) NOT NULL DEFAULT '',
  environment VARCHAR(255) NOT NULL DEFAULT '',
  cost_center VARCHAR(255) NOT NULL DEFAULT '',
  protected BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(6) NOT NULL,
  last_seen_at DATETIME(6) NOT NULL,
  raw JSON NOT NULL,
  UNIQUE KEY idx_resources_stable_id (provider, account_id, region, type, native_id),
  INDEX idx_resources_scan_id (scan_id),
  INDEX idx_resources_type (type),
  INDEX idx_resources_region (region),
  INDEX idx_resources_last_seen_at (last_seen_at),
  CONSTRAINT fk_resources_scan FOREIGN KEY (scan_id) REFERENCES scan_jobs(id)
);

CREATE TABLE IF NOT EXISTS resource_snapshots (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  scan_id VARCHAR(64) NOT NULL,
  resource_id VARCHAR(64) NOT NULL,
  raw JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  INDEX idx_resource_snapshots_scan_id (scan_id),
  INDEX idx_resource_snapshots_resource_id (resource_id),
  CONSTRAINT fk_resource_snapshots_scan FOREIGN KEY (scan_id) REFERENCES scan_jobs(id),
  CONSTRAINT fk_resource_snapshots_resource FOREIGN KEY (resource_id) REFERENCES resources(id)
);

CREATE TABLE IF NOT EXISTS resource_edges (
  id VARCHAR(64) PRIMARY KEY,
  scan_id VARCHAR(64) NOT NULL,
  source_resource_id VARCHAR(64) NOT NULL,
  target_resource_id VARCHAR(64) NOT NULL,
  type VARCHAR(128) NOT NULL,
  source VARCHAR(128) NOT NULL,
  confidence DOUBLE NOT NULL,
  evidence JSON NOT NULL,
  created_at DATETIME(6) NOT NULL,
  UNIQUE KEY idx_resource_edges_stable (scan_id, source_resource_id, target_resource_id, type),
  INDEX idx_resource_edges_scan_id (scan_id),
  INDEX idx_resource_edges_source_resource_id (source_resource_id),
  INDEX idx_resource_edges_target_resource_id (target_resource_id)
);

CREATE TABLE IF NOT EXISTS cleanup_candidates (
  id VARCHAR(64) PRIMARY KEY,
  scan_id VARCHAR(64) NOT NULL,
  resource_id VARCHAR(64) NOT NULL,
  rule_id VARCHAR(128) NOT NULL,
  reason TEXT NOT NULL,
  evidence JSON NOT NULL,
  confidence DOUBLE NOT NULL,
  risk VARCHAR(32) NOT NULL,
  recommended_action VARCHAR(64) NOT NULL,
  estimated_monthly_savings DOUBLE NOT NULL DEFAULT 0,
  status VARCHAR(64) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  UNIQUE KEY idx_cleanup_candidates_stable (scan_id, resource_id, rule_id),
  INDEX idx_cleanup_candidates_scan_id (scan_id),
  INDEX idx_cleanup_candidates_resource_id (resource_id),
  INDEX idx_cleanup_candidates_rule_id (rule_id),
  INDEX idx_cleanup_candidates_status (status)
);

CREATE TABLE IF NOT EXISTS cleanup_plans (
  id VARCHAR(64) PRIMARY KEY,
  status VARCHAR(64) NOT NULL,
  dry_run BOOLEAN NOT NULL,
  resource_count INT NOT NULL,
  risk VARCHAR(32) NOT NULL,
  estimated_monthly_savings DOUBLE NOT NULL DEFAULT 0,
  created_by VARCHAR(255) NOT NULL,
  approved_by VARCHAR(255) NOT NULL DEFAULT '',
  approval_comment TEXT NOT NULL,
  created_at DATETIME(6) NOT NULL,
  approved_at DATETIME(6) NULL,
  executed_at DATETIME(6) NULL,
  INDEX idx_cleanup_plans_status (status),
  INDEX idx_cleanup_plans_created_at (created_at)
);

CREATE TABLE IF NOT EXISTS cleanup_plan_items (
  id VARCHAR(64) PRIMARY KEY,
  plan_id VARCHAR(64) NOT NULL,
  candidate_id VARCHAR(64) NOT NULL,
  resource_id VARCHAR(64) NOT NULL,
  action VARCHAR(64) NOT NULL,
  item_order INT NOT NULL,
  risk VARCHAR(32) NOT NULL,
  blocked BOOLEAN NOT NULL DEFAULT FALSE,
  block_reason TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence JSON NOT NULL,
  estimated_monthly_savings DOUBLE NOT NULL DEFAULT 0,
  result VARCHAR(64) NOT NULL DEFAULT '',
  request_id VARCHAR(128) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  INDEX idx_cleanup_plan_items_plan_id (plan_id),
  INDEX idx_cleanup_plan_items_candidate_id (candidate_id),
  INDEX idx_cleanup_plan_items_resource_id (resource_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
  id VARCHAR(64) PRIMARY KEY,
  actor VARCHAR(255) NOT NULL,
  action VARCHAR(128) NOT NULL,
  target_type VARCHAR(128) NOT NULL,
  target_id VARCHAR(128) NOT NULL,
  result VARCHAR(64) NOT NULL,
  message TEXT NOT NULL,
  request_id VARCHAR(128) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  INDEX idx_audit_events_action (action),
  INDEX idx_audit_events_target (target_type, target_id),
  INDEX idx_audit_events_created_at (created_at)
);

-- +goose Down
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS cleanup_plan_items;
DROP TABLE IF EXISTS cleanup_plans;
DROP TABLE IF EXISTS cleanup_candidates;
DROP TABLE IF EXISTS resource_edges;
DROP TABLE IF EXISTS resource_snapshots;
DROP TABLE IF EXISTS resources;
DROP TABLE IF EXISTS scan_jobs;
DROP TABLE IF EXISTS accounts;
