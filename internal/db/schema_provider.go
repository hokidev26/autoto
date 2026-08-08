package db

// Schema for provider and remote-execution state: model client rows, provider
// secrets and account stats, remote execution, and per-account preferences.
//
// Split out of schema.go, which was over the size budget in scripts/check.sh. These
// constants are concatenated into the aggregate in schema.go and referenced by
// migrations.go; Go resolves them across files in the same package, so nothing that
// reads them had to change.

const modelClientSchemaSQL = `

CREATE TABLE IF NOT EXISTS model_aggregates (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  mode TEXT NOT NULL DEFAULT 'priority',
  revision INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  CHECK (mode = 'priority'),
  CHECK (revision >= 1),
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 120)
);

CREATE TABLE IF NOT EXISTS model_aggregate_members (
  aggregate_id TEXT NOT NULL REFERENCES model_aggregates(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  model_ref TEXT NOT NULL,
  PRIMARY KEY (aggregate_id, position),
  UNIQUE(aggregate_id, model_ref),
  CHECK (position >= 0),
  CHECK (length(CAST(model_ref AS BLOB)) BETWEEN 1 AND 256)
);
CREATE INDEX IF NOT EXISTS idx_model_aggregate_members_ref ON model_aggregate_members(model_ref, aggregate_id);

CREATE TABLE IF NOT EXISTS runtime_settings (
  id TEXT PRIMARY KEY CHECK (id = 'default'),
  installation_id TEXT NOT NULL UNIQUE,
  default_reasoning_effort TEXT NOT NULL DEFAULT 'auto',
  subscription_tier TEXT NOT NULL DEFAULT 'free',
  account_email TEXT,
  revision INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  CHECK (default_reasoning_effort IN ('auto', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra')),
  CHECK (subscription_tier IN ('free', 'plus', 'pro', 'team', 'enterprise', 'education_k12')),
  CHECK (account_email IS NULL OR length(CAST(account_email AS BLOB)) BETWEEN 3 AND 320),
  CHECK (revision >= 1),
  CHECK (length(CAST(installation_id AS BLOB)) = 36)
);
`

const providerSecretsSchemaSQL = `

CREATE TABLE IF NOT EXISTS provider_secrets (
  provider_name TEXT NOT NULL,
  secret_kind TEXT NOT NULL,
  active_ciphertext BLOB,
  active_nonce BLOB,
  active_binding_fingerprint BLOB,
  active_key_version INTEGER NOT NULL DEFAULT 0,
  active_last_five TEXT NOT NULL DEFAULT '',
  active_secret_revision INTEGER NOT NULL DEFAULT 0,
  pending_action TEXT NOT NULL DEFAULT 'none',
  pending_ciphertext BLOB,
  pending_nonce BLOB,
  pending_binding_fingerprint BLOB,
  pending_key_version INTEGER NOT NULL DEFAULT 0,
  pending_last_five TEXT NOT NULL DEFAULT '',
  pending_secret_revision INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider_name, secret_kind),
  CHECK (length(CAST(provider_name AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(secret_kind AS BLOB)) BETWEEN 1 AND 128),
  CHECK (active_key_version >= 0),
  CHECK (active_secret_revision >= 0),
  CHECK (pending_action IN ('none', 'set', 'clear', 'delete')),
  CHECK (pending_key_version >= 0),
  CHECK (pending_secret_revision >= 0),
  CHECK (pending_action <> 'set' OR pending_ciphertext IS NOT NULL),
  CHECK (pending_action <> 'set' OR pending_nonce IS NOT NULL),
  CHECK (pending_action <> 'set' OR pending_key_version > 0),
  CHECK (pending_action <> 'set' OR pending_binding_fingerprint IS NOT NULL),
  CHECK (pending_action NOT IN ('set', 'clear') OR pending_secret_revision > 0),
  CHECK (pending_action <> 'clear' OR pending_binding_fingerprint IS NOT NULL),
  CHECK (active_ciphertext IS NULL OR active_nonce IS NOT NULL),
  CHECK (active_ciphertext IS NULL OR active_binding_fingerprint IS NOT NULL),
  CHECK (active_ciphertext IS NULL OR active_key_version > 0),
  CHECK (active_ciphertext IS NULL OR active_secret_revision > 0)
);
CREATE INDEX IF NOT EXISTS idx_provider_secrets_updated ON provider_secrets(updated_at DESC, provider_name, secret_kind);
`

const providerAccountStatsSchemaSQL = `

CREATE TABLE IF NOT EXISTS provider_account_stats (
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  success_count INTEGER NOT NULL DEFAULT 0,
  failure_count INTEGER NOT NULL DEFAULT 0,
  last_attempt_at TEXT,
  last_use_at TEXT,
  last_success_at TEXT,
  last_failure_at TEXT,
  last_http_status INTEGER,
  last_status_code TEXT,
  last_error_code TEXT,
  quota_snapshot_json TEXT,
  quota_fetched_at TEXT,
  PRIMARY KEY (provider, account_id),
  CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(account_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (success_count >= 0),
  CHECK (failure_count >= 0),
  CHECK (last_http_status IS NULL OR last_http_status BETWEEN 100 AND 599),
  CHECK (last_status_code IS NULL OR length(CAST(last_status_code AS BLOB)) <= 128),
  CHECK (last_error_code IS NULL OR length(CAST(last_error_code AS BLOB)) <= 128),
  CHECK (quota_snapshot_json IS NULL OR (length(CAST(quota_snapshot_json AS BLOB)) <= 65536 AND json_valid(quota_snapshot_json) AND json_type(quota_snapshot_json) = 'object'))
);
CREATE INDEX IF NOT EXISTS idx_provider_account_stats_last_use ON provider_account_stats(provider, last_use_at DESC, account_id);
`

const remoteExecutionSchemaSQL = `

CREATE TABLE IF NOT EXISTS execution_devices (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  name TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'disabled',
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  identity_fingerprint TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (kind IN ('local', 'remote')),
  CHECK (enabled IN (0, 1)),
  CHECK (status IN ('disabled', 'unknown', 'offline', 'online', 'ready', 'degraded', 'error')),
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 120),
  CHECK (length(CAST(capabilities_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (json_valid(capabilities_json) AND json_type(capabilities_json) = 'object'),
  CHECK ((kind = 'local' AND identity_fingerprint IS NULL) OR (kind = 'remote' AND length(CAST(identity_fingerprint AS BLOB)) BETWEEN 16 AND 512))
);
CREATE INDEX IF NOT EXISTS idx_execution_devices_kind_enabled ON execution_devices(kind, enabled, status, id);

INSERT OR IGNORE INTO execution_devices (id, kind, name, enabled, status, capabilities_json, identity_fingerprint, created_at, updated_at)
VALUES ('local', 'local', 'Local', 1, 'ready', '{}', NULL, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now'));

CREATE TABLE IF NOT EXISTS project_device_grants (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES execution_devices(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 0,
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (project_id, device_id),
  CHECK (enabled IN (0, 1)),
  CHECK (length(CAST(capabilities_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (json_valid(capabilities_json) AND json_type(capabilities_json) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_project_device_grants_device ON project_device_grants(device_id, enabled, project_id);

CREATE TABLE IF NOT EXISTS remote_execution_tasks (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  execution_device_id TEXT NOT NULL REFERENCES execution_devices(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'queued',
  payload_json TEXT NOT NULL DEFAULT '{}',
  result_json TEXT NOT NULL DEFAULT '{}',
  no_fallback INTEGER NOT NULL DEFAULT 1,
  lease_owner TEXT,
  lease_until TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  CHECK (status IN ('queued', 'leased', 'running', 'succeeded', 'failed', 'cancelled', 'expired')),
  CHECK (no_fallback = 1),
  CHECK (attempt_count >= 0),
  CHECK (revision >= 1),
  CHECK (length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(payload_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (length(CAST(result_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
  CHECK (json_valid(result_json) AND json_type(result_json) = 'object'),
  CHECK (
    (status = 'queued' AND lease_owner IS NULL AND lease_until IS NULL AND completed_at IS NULL)
    OR (status IN ('leased', 'running') AND lease_owner IS NOT NULL AND lease_until IS NOT NULL AND completed_at IS NULL)
    OR (status IN ('succeeded', 'failed', 'cancelled', 'expired') AND lease_owner IS NULL AND lease_until IS NULL AND completed_at IS NOT NULL)
  ),
  CHECK (status <> 'failed' OR length(CAST(last_error AS BLOB)) BETWEEN 1 AND 4096)
);
CREATE INDEX IF NOT EXISTS idx_remote_execution_tasks_claim ON remote_execution_tasks(execution_device_id, status, lease_until, created_at, id);
CREATE INDEX IF NOT EXISTS idx_remote_execution_tasks_agent ON remote_execution_tasks(agent_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_remote_execution_tasks_project ON remote_execution_tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_remote_execution_tasks_run ON remote_execution_tasks(run_id);

CREATE TABLE IF NOT EXISTS transfer_jobs (
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  execution_device_id TEXT NOT NULL REFERENCES execution_devices(id) ON DELETE RESTRICT,
  direction TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  payload_json TEXT NOT NULL DEFAULT '{}',
  result_json TEXT NOT NULL DEFAULT '{}',
  no_fallback INTEGER NOT NULL DEFAULT 1,
  lease_owner TEXT,
  lease_until TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  CHECK (direction IN ('upload', 'download')),
  CHECK (status IN ('queued', 'leased', 'transferring', 'completed', 'failed', 'cancelled', 'expired')),
  CHECK (no_fallback = 1),
  CHECK (attempt_count >= 0),
  CHECK (revision >= 1),
  CHECK (length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(payload_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (length(CAST(result_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
  CHECK (json_valid(result_json) AND json_type(result_json) = 'object'),
  CHECK (
    (status = 'queued' AND lease_owner IS NULL AND lease_until IS NULL AND completed_at IS NULL)
    OR (status IN ('leased', 'transferring') AND lease_owner IS NOT NULL AND lease_until IS NOT NULL AND completed_at IS NULL)
    OR (status IN ('completed', 'failed', 'cancelled', 'expired') AND lease_owner IS NULL AND lease_until IS NULL AND completed_at IS NOT NULL)
  ),
  CHECK (status <> 'failed' OR length(CAST(last_error AS BLOB)) BETWEEN 1 AND 4096)
);
CREATE INDEX IF NOT EXISTS idx_transfer_jobs_claim ON transfer_jobs(execution_device_id, status, lease_until, created_at, id);
CREATE INDEX IF NOT EXISTS idx_transfer_jobs_project ON transfer_jobs(project_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_agents_execution_device ON agents(execution_device_id, id);
CREATE INDEX IF NOT EXISTS idx_runs_execution_device ON runs(execution_device_id, execution_generation, id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_execution_device ON agent_tool_calls(execution_device_id, created_at, id);

CREATE TRIGGER IF NOT EXISTS agents_execution_device_insert BEFORE INSERT ON agents
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid agent execution device'); END;
CREATE TRIGGER IF NOT EXISTS agents_execution_device_update BEFORE UPDATE OF execution_device_id ON agents
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid agent execution device'); END;
CREATE TRIGGER IF NOT EXISTS runs_execution_device_insert BEFORE INSERT ON runs
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid run execution device'); END;
CREATE TRIGGER IF NOT EXISTS runs_execution_device_update BEFORE UPDATE OF execution_device_id ON runs
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid run execution device'); END;
CREATE TRIGGER IF NOT EXISTS tool_calls_execution_device_insert BEFORE INSERT ON agent_tool_calls
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid tool call execution device'); END;
CREATE TRIGGER IF NOT EXISTS tool_calls_execution_device_update BEFORE UPDATE OF execution_device_id ON agent_tool_calls
WHEN NEW.execution_device_id IS NULL OR length(CAST(NEW.execution_device_id AS BLOB)) NOT BETWEEN 1 AND 128 OR NOT EXISTS (SELECT 1 FROM execution_devices WHERE id = NEW.execution_device_id)
BEGIN SELECT RAISE(ABORT, 'invalid tool call execution device'); END;
`

const accountPreferencesSchemaSQL = `

CREATE TABLE IF NOT EXISTS account_preferences (
  scope_kind TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  profile_json TEXT NOT NULL DEFAULT '{}',
  preferred_model TEXT NOT NULL DEFAULT '',
  model_visibility_json TEXT NOT NULL DEFAULT '{}',
  setup_version INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  local_storage_import_version INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (scope_kind, scope_id),
  CHECK (scope_kind IN ('instance', 'user')),
  CHECK ((scope_kind = 'instance' AND scope_id = 'default') OR (scope_kind = 'user' AND length(CAST(scope_id AS BLOB)) BETWEEN 1 AND 128)),
  CHECK (revision >= 1),
  CHECK (setup_version BETWEEN 0 AND 1000),
  CHECK (local_storage_import_version IN (0, 1)),
  CHECK (json_valid(profile_json)),
  CHECK (json_valid(model_visibility_json)),
  CHECK (length(CAST(preferred_model AS BLOB)) <= 1024),
  CHECK (length(CAST(profile_json AS BLOB)) + length(CAST(preferred_model AS BLOB)) + length(CAST(model_visibility_json AS BLOB)) <= 262144)
);

CREATE TABLE IF NOT EXISTS account_preference_claims (
  claim_kind TEXT PRIMARY KEY,
  claimed_user_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  CHECK (claim_kind = 'instance_to_first_user'),
  CHECK (length(CAST(claimed_user_id AS BLOB)) BETWEEN 1 AND 128)
);
`
