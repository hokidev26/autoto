package db

const schemaSQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  handle TEXT,
  handle_key TEXT UNIQUE,
  password_hash TEXT,
  role TEXT NOT NULL DEFAULT 'user',
  avatar_color TEXT,
  avatar_image_id TEXT,
  git_username TEXT,
  git_email TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id);

CREATE TABLE IF NOT EXISTS message_drafts (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  content_text TEXT NOT NULL,
  version INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_message_drafts_agent ON message_drafts(agent_id);

CREATE TABLE IF NOT EXISTS agent_message_queue (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  created_by TEXT NOT NULL DEFAULT '',
  content_text TEXT NOT NULL,
  run_mode TEXT NOT NULL DEFAULT '',
  run_context TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_message_queue_agent ON agent_message_queue(agent_id, position, id);

-- Files parked with a queued message live here rather than in the browser so
-- the server can send them with no page open. The CHECK constraints are copied
-- from agent_message_attachments on purpose: a row admitted here that the
-- destination table would reject becomes a poison pill that fails on every
-- drain and blocks the queue behind it, so the same rules apply at park time.
CREATE TABLE IF NOT EXISTS agent_queued_message_attachments (
  id TEXT PRIMARY KEY,
  queued_message_id TEXT NOT NULL REFERENCES agent_message_queue(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  filename TEXT NOT NULL,
  mime_type TEXT,
  kind TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  data_blob BLOB NOT NULL,
  model_data_blob BLOB,
  model_mime_type TEXT,
  image_width INTEGER NOT NULL DEFAULT 0,
  image_height INTEGER NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL DEFAULT '',
  processing_status TEXT NOT NULL DEFAULT '',
  processing_code TEXT NOT NULL DEFAULT '',
  processing_error TEXT NOT NULL DEFAULT '',
  extracted_text TEXT,
  created_at TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  CHECK (image_width BETWEEN 0 AND 8192),
  CHECK (image_height BETWEEN 0 AND 8192),
  CHECK (processing_status IN ('', 'ready', 'rejected')),
  CHECK (length(COALESCE(model_data_blob, X'')) <= 4194304),
  CHECK (
    (processing_status = 'ready' AND length(COALESCE(model_data_blob, X'')) > 0 AND model_mime_type IN ('image/png', 'image/jpeg') AND image_width > 0 AND image_height > 0 AND length(sha256) = 64 AND processing_code = '' AND processing_error = '')
    OR (processing_status = 'rejected' AND length(COALESCE(model_data_blob, X'')) = 0 AND COALESCE(model_mime_type, '') = '' AND image_width = 0 AND image_height = 0 AND length(sha256) = 64 AND processing_code <> '' AND processing_error <> '')
    OR (processing_status = '' AND length(COALESCE(model_data_blob, X'')) = 0 AND COALESCE(model_mime_type, '') = '' AND image_width = 0 AND image_height = 0 AND sha256 = '' AND processing_code = '' AND processing_error = '')
  )
);
CREATE INDEX IF NOT EXISTS idx_agent_queued_message_attachments_parent ON agent_queued_message_attachments(queued_message_id, position, id);

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  flow_mode TEXT NOT NULL DEFAULT 'workspace',
  git_path TEXT,
  remote_url TEXT,
  default_branch TEXT,
  startup_script TEXT,
  copy_files TEXT,
  workline_settings TEXT,
  proxy_domain TEXT,
  pinned INTEGER NOT NULL DEFAULT 0,
  archived_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (pinned IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_projects_navigation_state ON projects(pinned DESC, archived_at, updated_at DESC, id ASC);

CREATE TABLE IF NOT EXISTS project_members (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(project_id, user_id),
  CHECK (role IN ('owner', 'member'))
);
CREATE INDEX IF NOT EXISTS idx_project_members_user ON project_members(user_id, project_id);

CREATE TABLE IF NOT EXISTS worklines (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  role TEXT NOT NULL DEFAULT 'root',
  branch TEXT,
  worktree_path TEXT,
  base_branch TEXT,
  parent_workline_id TEXT REFERENCES worklines(id) ON DELETE SET NULL,
  fork_point TEXT,
  merged_into_workline_id TEXT REFERENCES worklines(id) ON DELETE SET NULL,
  merge_commit_sha TEXT,
  merge_strategy TEXT,
  pre_merge_target_sha TEXT,
  container_config TEXT,
  exploration_group_id TEXT,
  is_root INTEGER NOT NULL DEFAULT 0,
  head_commit_sha TEXT,
  start_commit_sha TEXT,
  commit_count INTEGER NOT NULL DEFAULT 0,
  color TEXT,
  group_label TEXT,
  pinned INTEGER NOT NULL DEFAULT 0,
  position_x REAL,
  position_y REAL,
  panel_expanded INTEGER,
  panel_width REAL,
  panel_height REAL,
  review_source_workline_id TEXT,
  review_status TEXT,
  last_accessed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_worklines_project ON worklines(project_id);
CREATE INDEX IF NOT EXISTS idx_worklines_parent_workline ON worklines(parent_workline_id);
CREATE INDEX IF NOT EXISTS idx_worklines_merged_into_workline ON worklines(merged_into_workline_id);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  workline_id TEXT REFERENCES worklines(id) ON DELETE SET NULL,
  api_conversation_id TEXT,
  fork_message_id TEXT,
  type TEXT NOT NULL DEFAULT 'primary',
  subagent_type TEXT,
  title TEXT NOT NULL,
  inherit_mode TEXT,
  parent_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  context_summary TEXT,
  model TEXT NOT NULL,
  system_prompt TEXT,
  permission_mode TEXT NOT NULL DEFAULT 'acceptEdits',
  previous_permission_mode TEXT,
  entity_generation INTEGER NOT NULL DEFAULT 1,
  permission_generation INTEGER NOT NULL DEFAULT 1,
  execution_generation INTEGER NOT NULL DEFAULT 0,
  reasoning_effort TEXT,
  execution_device_id TEXT NOT NULL DEFAULT 'local',
  fast_mode INTEGER NOT NULL DEFAULT 0,
  relaxed_plan INTEGER NOT NULL DEFAULT 0,
  message_count INTEGER NOT NULL DEFAULT 0,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  last_message_at TEXT,
  status TEXT NOT NULL DEFAULT 'idle',
  plan_mode INTEGER NOT NULL DEFAULT 0,
  cwd TEXT,
  error_message TEXT,
  todos_json TEXT,
  todos_tool_use_id TEXT,
  prune_boundary_message_id TEXT,
  pruned_percent INTEGER,
  prune_enabled INTEGER NOT NULL DEFAULT 0,
  is_background INTEGER NOT NULL DEFAULT 0,
  background_status TEXT,
  background_result TEXT,
  background_completed_at TEXT,
  pinned INTEGER NOT NULL DEFAULT 0,
  archived_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (execution_generation >= 0),
  CHECK (pinned IN (0, 1)),
  CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('auto', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra')),
  CHECK (length(CAST(execution_device_id AS BLOB)) BETWEEN 1 AND 128)
);
CREATE INDEX IF NOT EXISTS idx_agents_workline ON agents(workline_id);
CREATE INDEX IF NOT EXISTS idx_agents_parent ON agents(parent_agent_id);
CREATE INDEX IF NOT EXISTS idx_agents_navigation_state ON agents(pinned DESC, archived_at, updated_at DESC, id ASC);

CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  trigger_message_id TEXT REFERENCES agent_messages(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  started_at TEXT,
  completed_at TEXT,
  error_message TEXT,
  base_head TEXT,
  end_head TEXT,
  checkpoint_repo_root TEXT,
  git_snapshot_at TEXT,
  checkpoint_state TEXT NOT NULL DEFAULT 'none',
  checkpoint_error TEXT,
  rolled_back_at TEXT,
  source TEXT NOT NULL DEFAULT 'manual',
  source_id TEXT NOT NULL DEFAULT '',
  permission_mode_cap TEXT NOT NULL DEFAULT '',
  execution_generation INTEGER NOT NULL DEFAULT 0,
  dispatch_id TEXT,
  duration_ms INTEGER,
  trigger_type TEXT NOT NULL DEFAULT 'manual',
  execution_device_id TEXT NOT NULL DEFAULT 'local',
  execution_mode TEXT NOT NULL DEFAULT 'execute',
  plan_id TEXT REFERENCES plans(id) ON DELETE SET NULL,
  policy_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  agent_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  tool_catalog_digest TEXT NOT NULL DEFAULT '',
  workspace_fingerprint TEXT NOT NULL DEFAULT '',
  auto_continuation_mode TEXT NOT NULL DEFAULT 'off',
  continuation_count INTEGER NOT NULL DEFAULT 0,
  continuation_segment_turns INTEGER NOT NULL DEFAULT 0,
  turn_count INTEGER NOT NULL DEFAULT 0,
  max_total_turns INTEGER NOT NULL DEFAULT 0,
  max_continuations INTEGER NOT NULL DEFAULT 0,
  max_total_tokens INTEGER NOT NULL DEFAULT 0,
  consumed_input_tokens INTEGER NOT NULL DEFAULT 0,
  consumed_output_tokens INTEGER NOT NULL DEFAULT 0,
  deadline_at TEXT,
  resume_after_message_id TEXT REFERENCES agent_messages(id) ON DELETE SET NULL,
  last_stop_reason TEXT,
  continuation_reason TEXT,
  waiting_background_task_id TEXT REFERENCES background_tasks(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (permission_mode_cap IN ('', 'readOnly', 'acceptEdits', 'bypassPermissions')),
  CHECK (execution_generation >= 0),
  CHECK (dispatch_id IS NULL OR length(CAST(dispatch_id AS BLOB)) BETWEEN 1 AND 256),
  CHECK (duration_ms IS NULL OR duration_ms >= 0),
  CHECK (trigger_type IN ('manual', 'scheduled', 'goal', 'internal')),
  CHECK (length(CAST(execution_device_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (execution_mode IN ('plan', 'execute')),
  CHECK (execution_mode = 'execute' OR plan_id IS NULL),
  CHECK (policy_generation_snapshot >= 0),
  CHECK (agent_generation_snapshot >= 0),
  CHECK (length(CAST(tool_catalog_digest AS BLOB)) <= 512),
  CHECK (length(CAST(workspace_fingerprint AS BLOB)) <= 512),
  CHECK (auto_continuation_mode IN ('off', 'safe')),
  CHECK (continuation_count >= 0),
  CHECK (continuation_segment_turns >= 0),
  CHECK (turn_count >= 0),
  CHECK (max_total_turns >= 0),
  CHECK (max_continuations >= 0),
  CHECK (max_total_tokens >= 0),
  CHECK (consumed_input_tokens >= 0),
  CHECK (consumed_output_tokens >= 0),
  CHECK (length(CAST(COALESCE(last_stop_reason, '') AS BLOB)) <= 256),
  CHECK (length(CAST(COALESCE(continuation_reason, '') AS BLOB)) <= 4096)
);
CREATE INDEX IF NOT EXISTS idx_runs_agent_started ON runs(agent_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_plan ON runs(plan_id, execution_generation DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_agent_execution_generation ON runs(agent_id, execution_generation) WHERE execution_generation > 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_dispatch_id ON runs(dispatch_id) WHERE dispatch_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_runs_agent_execution_after ON runs(agent_id, execution_generation, id);
CREATE INDEX IF NOT EXISTS idx_runs_continuation_pending ON runs(status, updated_at ASC, id ASC) WHERE status = 'continuation_pending';
CREATE INDEX IF NOT EXISTS idx_runs_waiting_background_task ON runs(waiting_background_task_id, status, id) WHERE waiting_background_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_runs_trigger_message ON runs(trigger_message_id);
CREATE INDEX IF NOT EXISTS idx_runs_resume_after_message ON runs(resume_after_message_id);
CREATE INDEX IF NOT EXISTS idx_runs_waiting_background_task_fk ON runs(waiting_background_task_id);
CREATE TRIGGER IF NOT EXISTS trg_runs_auto_continuation_mode_insert
BEFORE INSERT ON runs FOR EACH ROW
WHEN NEW.auto_continuation_mode NOT IN ('off', 'safe')
BEGIN SELECT RAISE(ABORT, 'invalid auto_continuation_mode'); END;
CREATE TRIGGER IF NOT EXISTS trg_runs_auto_continuation_mode_update
BEFORE UPDATE OF auto_continuation_mode ON runs FOR EACH ROW
WHEN NEW.auto_continuation_mode NOT IN ('off', 'safe')
BEGIN SELECT RAISE(ABORT, 'invalid auto_continuation_mode'); END;

CREATE TABLE IF NOT EXISTS run_git_changes (
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  orig_path TEXT,
  index_status TEXT NOT NULL,
  worktree_status TEXT NOT NULL,
  untracked INTEGER NOT NULL DEFAULT 0,
  index_fingerprint TEXT,
  worktree_fingerprint TEXT NOT NULL,
  PRIMARY KEY (run_id, path)
);
CREATE INDEX IF NOT EXISTS idx_run_git_changes_run ON run_git_changes(run_id, path);

CREATE TABLE IF NOT EXISTS agent_messages (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  sdk_message_uuid TEXT,
  parent_tool_use_id TEXT,
  role TEXT NOT NULL,
  content_json TEXT,
  provider_state_json TEXT,
  content_text TEXT,
  reasoning_text TEXT,
  tokens_in INTEGER,
  cost_usd REAL,
  turn_usage_json TEXT,
  context_percent REAL,
  meter_usage REAL,
  meter_unit TEXT,
  commit_sha TEXT,
  command_text TEXT,
  correction_of_message_id TEXT REFERENCES agent_messages(id) ON DELETE RESTRICT,
  superseded_at TEXT,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  completion_state TEXT,
  stop_reason TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_messages_agent_time ON agent_messages(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_run ON agent_messages(run_id, created_at);
-- Superseded messages are excluded from every model context build, which walks
-- a conversation in (created_at, id) order, so the filter needs to be cheap on
-- exactly that path.
CREATE INDEX IF NOT EXISTS idx_agent_messages_superseded ON agent_messages(agent_id, superseded_at) WHERE superseded_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_messages_correction ON agent_messages(correction_of_message_id);
CREATE INDEX IF NOT EXISTS idx_agent_messages_created_by ON agent_messages(created_by);

CREATE TABLE IF NOT EXISTS agent_message_attachments (
  id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL REFERENCES agent_messages(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  filename TEXT NOT NULL,
  mime_type TEXT,
  kind TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  data_blob BLOB NOT NULL,
  model_data_blob BLOB,
  model_mime_type TEXT,
  image_width INTEGER NOT NULL DEFAULT 0,
  image_height INTEGER NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL DEFAULT '',
  processing_status TEXT NOT NULL DEFAULT '',
  processing_code TEXT NOT NULL DEFAULT '',
  processing_error TEXT NOT NULL DEFAULT '',
  extracted_text TEXT,
  created_at TEXT NOT NULL,
  CHECK (image_width BETWEEN 0 AND 8192),
  CHECK (image_height BETWEEN 0 AND 8192),
  CHECK (processing_status IN ('', 'ready', 'rejected')),
  CHECK (length(COALESCE(model_data_blob, X'')) <= 4194304),
  CHECK (
    (processing_status = 'ready' AND length(COALESCE(model_data_blob, X'')) > 0 AND model_mime_type IN ('image/png', 'image/jpeg') AND image_width > 0 AND image_height > 0 AND length(sha256) = 64 AND processing_code = '' AND processing_error = '')
    OR (processing_status = 'rejected' AND length(COALESCE(model_data_blob, X'')) = 0 AND COALESCE(model_mime_type, '') = '' AND image_width = 0 AND image_height = 0 AND length(sha256) = 64 AND processing_code <> '' AND processing_error <> '')
    OR (processing_status = '' AND length(COALESCE(model_data_blob, X'')) = 0 AND COALESCE(model_mime_type, '') = '' AND image_width = 0 AND image_height = 0 AND sha256 = '' AND processing_code = '' AND processing_error = '')
  )
);
CREATE INDEX IF NOT EXISTS idx_message_attachments_message ON agent_message_attachments(message_id, created_at);
CREATE INDEX IF NOT EXISTS idx_message_attachments_agent ON agent_message_attachments(agent_id, created_at);

CREATE TABLE IF NOT EXISTS agent_tool_calls (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  message_id TEXT REFERENCES agent_messages(id) ON DELETE SET NULL,
  tool_use_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  input_json TEXT,
  output_json TEXT,
  status TEXT NOT NULL,
  duration_ms INTEGER,
  error_message TEXT,
  permission_decided_by TEXT,
  permission_decided_at TEXT,
  permission_deny_message TEXT,
  permission_decision_reason TEXT,
  permission_suggestions TEXT,
  permission_generation INTEGER NOT NULL DEFAULT 1,
  policy_generation INTEGER NOT NULL DEFAULT 1,
  execution_device_id TEXT NOT NULL DEFAULT 'local',
  is_background INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER,
  output_tokens INTEGER,
  total_cost REAL,
  provider TEXT,
  model TEXT,
  result_message_id TEXT,
  started_at TEXT,
  completed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (length(CAST(execution_device_id AS BLOB)) BETWEEN 1 AND 128)
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_agent ON agent_tool_calls(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_calls_run ON agent_tool_calls(run_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tool_calls_tool_use ON agent_tool_calls(agent_id, tool_use_id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_run_updated ON agent_tool_calls(run_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_message ON agent_tool_calls(message_id);

CREATE TABLE IF NOT EXISTS api_requests (
  id TEXT PRIMARY KEY,
  agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  message_id TEXT REFERENCES agent_messages(id) ON DELETE SET NULL,
  kind TEXT NOT NULL DEFAULT 'model',
  provider TEXT,
  credential_id TEXT,
  gateway_key_id TEXT REFERENCES gateway_keys(id) ON DELETE SET NULL,
  model TEXT,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cached_input_tokens INTEGER,
  reasoning_tokens INTEGER,
  ttft_ms INTEGER,
  duration_ms INTEGER,
  cost_usd REAL,
  context_percent REAL,
  meter_usage REAL,
  meter_unit TEXT,
  error_message TEXT,
  raw_dump_json TEXT,
  stop_reason TEXT,
  turn_index INTEGER NOT NULL DEFAULT 0,
  continuation_index INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  CHECK (turn_index >= 0),
  CHECK (continuation_index >= 0)
);
CREATE INDEX IF NOT EXISTS idx_api_requests_run ON api_requests(run_id, created_at);
CREATE INDEX IF NOT EXISTS idx_api_requests_created ON api_requests(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_requests_provider_model_created ON api_requests(provider, model, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_requests_gateway_key_created ON api_requests(gateway_key_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_requests_agent ON api_requests(agent_id);
CREATE INDEX IF NOT EXISTS idx_api_requests_message ON api_requests(message_id);

CREATE TABLE IF NOT EXISTS agent_backends (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'local',
  base_url TEXT NOT NULL,
  api_key TEXT,
  active INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_backends_active ON agent_backends(active);

CREATE TABLE IF NOT EXISTS mcp_servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  transport TEXT NOT NULL DEFAULT 'stdio',
  command TEXT NOT NULL,
  args_json TEXT,
  cwd TEXT,
  env_json TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled);

CREATE TABLE IF NOT EXISTS skills (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  command TEXT NOT NULL COLLATE NOCASE,
  description TEXT NOT NULL,
  prompt TEXT NOT NULL,
  source TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT 'global',
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  workline_id TEXT REFERENCES worklines(id) ON DELETE CASCADE,
  deleted_at TEXT,
  revision_no INTEGER NOT NULL DEFAULT 1,
  content_hash TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  scan_verdict TEXT NOT NULL,
  scan_findings_json TEXT NOT NULL DEFAULT '[]',
  scanner_version INTEGER NOT NULL DEFAULT 0,
  risk_acknowledged_at TEXT,
  risk_acknowledged_by TEXT,
  risk_acknowledged_hash TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (source IN ('manual', 'local_migration', 'skill_md')),
  CHECK (scope IN ('global', 'project', 'workspace')),
  CHECK ((scope = 'global' AND project_id IS NULL AND workline_id IS NULL) OR (scope = 'project' AND project_id IS NOT NULL AND workline_id IS NULL) OR (scope = 'workspace' AND project_id IS NOT NULL AND workline_id IS NOT NULL)),
  CHECK (revision_no >= 1),
  CHECK (scan_verdict IN ('safe', 'review', 'blocked')),
  CHECK (enabled IN (0, 1)),
  CHECK (NOT (scan_verdict = 'blocked' AND enabled = 1)),
  CHECK (NOT (scan_verdict = 'review' AND enabled = 1 AND (TRIM(COALESCE(risk_acknowledged_at, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = '' OR TRIM(COALESCE(risk_acknowledged_by, ''), ' ' || char(9) || char(10) || char(11) || char(12) || char(13)) = '' OR COALESCE(risk_acknowledged_hash, '') <> content_hash)))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_global_command ON skills(command COLLATE NOCASE) WHERE deleted_at IS NULL AND scope = 'global';
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_project_command ON skills(project_id, command COLLATE NOCASE) WHERE deleted_at IS NULL AND scope = 'project';
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_workspace_command ON skills(workline_id, command COLLATE NOCASE) WHERE deleted_at IS NULL AND scope = 'workspace';
CREATE INDEX IF NOT EXISTS idx_skills_scope_command ON skills(scope, project_id, workline_id, command COLLATE NOCASE, id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_skills_project ON skills(project_id);
CREATE INDEX IF NOT EXISTS idx_skills_workline ON skills(workline_id);

CREATE TRIGGER IF NOT EXISTS skills_workspace_scope_insert
BEFORE INSERT ON skills
WHEN NEW.scope = 'workspace' AND NOT EXISTS (SELECT 1 FROM worklines WHERE id = NEW.workline_id AND project_id = NEW.project_id)
BEGIN
  SELECT RAISE(ABORT, 'workspace skill must reference a workline in the selected project');
END;
CREATE TRIGGER IF NOT EXISTS skills_workspace_scope_update
BEFORE UPDATE OF scope, project_id, workline_id ON skills
WHEN NEW.scope = 'workspace' AND NOT EXISTS (SELECT 1 FROM worklines WHERE id = NEW.workline_id AND project_id = NEW.project_id)
BEGIN
  SELECT RAISE(ABORT, 'workspace skill must reference a workline in the selected project');
END;

CREATE TABLE IF NOT EXISTS skill_revisions (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  revision_no INTEGER NOT NULL,
  operation TEXT NOT NULL,
  actor TEXT NOT NULL,
  restored_from_revision_no INTEGER,
  name TEXT NOT NULL,
  command TEXT NOT NULL COLLATE NOCASE,
  description TEXT NOT NULL,
  prompt TEXT NOT NULL,
  source TEXT NOT NULL,
  scope TEXT NOT NULL,
  project_id TEXT,
  workline_id TEXT,
  deleted_at TEXT,
  content_hash TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  scan_verdict TEXT NOT NULL,
  scan_findings_json TEXT NOT NULL,
  scanner_version INTEGER NOT NULL,
  risk_acknowledged_at TEXT,
  risk_acknowledged_by TEXT,
  risk_acknowledged_hash TEXT,
  head_created_at TEXT NOT NULL,
  head_updated_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(skill_id, revision_no),
  CHECK (operation IN ('baseline', 'create', 'update', 'delete', 'restore', 'revalidate')),
  CHECK (scope IN ('global', 'project', 'workspace')),
  CHECK (enabled IN (0, 1)),
  CHECK (scan_verdict IN ('safe', 'review', 'blocked'))
);
CREATE INDEX IF NOT EXISTS idx_skill_revisions_skill_revision ON skill_revisions(skill_id, revision_no DESC);
CREATE INDEX IF NOT EXISTS idx_skill_revisions_snapshot ON skill_revisions(sequence, skill_id);

CREATE TABLE IF NOT EXISTS skill_audit_events (
  id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  actor TEXT NOT NULL,
  skill_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  scan_verdict TEXT NOT NULL,
  finding_codes_json TEXT NOT NULL DEFAULT '[]',
  risk_acknowledged_at TEXT,
  created_at TEXT NOT NULL,
  CHECK (action IN ('create', 'update', 'enable', 'disable', 'delete', 'restore'))
);
CREATE INDEX IF NOT EXISTS idx_skill_audit_events_skill_created ON skill_audit_events(skill_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS notification_settings (
  id TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 0,
  webhook_url TEXT,
  notify_on_approval INTEGER NOT NULL DEFAULT 1,
  notify_on_done INTEGER NOT NULL DEFAULT 1,
  notify_on_error INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_preferences (
  id TEXT PRIMARY KEY,
  require_confirmation_for_exec INTEGER NOT NULL DEFAULT 1,
  require_confirmation_for_writes INTEGER NOT NULL DEFAULT 0,
  allow_read_only_by_default INTEGER NOT NULL DEFAULT 1,
  danger_reflection_enabled INTEGER NOT NULL DEFAULT 1,
  danger_reflection_level TEXT NOT NULL DEFAULT 'medium',
  policy_generation INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tool_permission_rules (
  id TEXT PRIMARY KEY,
  mode TEXT NOT NULL DEFAULT '*',
  tool_name TEXT NOT NULL DEFAULT '*',
  risk TEXT NOT NULL DEFAULT '*',
  decision TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  description TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tool_permission_rules_match ON tool_permission_rules(enabled, mode, tool_name, risk, priority);
` + automationAuditSchemaSQL + integrationConnectionsSchemaSQL + memorySchemaSQL + schedulesSchemaSQL + notificationDeliveriesSchemaSQL + channelPersistenceSchemaSQL + deviceActionRequestsSchemaSQL + specSchemaSQL + modelClientSchemaSQL + remoteExecutionSchemaSQL + providerAccountStatsSchemaSQL + providerSecretsSchemaSQL + pluginSchemaSQL + backgroundTaskSchemaSQL + planSchemaSQL + gatewaySchemaSQL + accountPreferencesSchemaSQL + oauthAppSchemaSQL + gatewayAccountGrantsSchemaSQL + generatedImagesSchemaSQL + toolExecutionGroupSchemaSQL + profileConfigurationSchemaSQL + remoteCollaborationSchemaSQL

const profileConfigurationSchemaSQL = toolAvailabilitySchemaSQL + agentRoleDefinitionSchemaSQL + promptDefinitionSchemaSQL + lifecycleHookSchemaSQL

const generatedImagesSchemaSQL = `

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_messages_agent_id ON agent_messages(agent_id, id);
CREATE TABLE IF NOT EXISTS agent_message_generated_images (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  message_id TEXT NOT NULL,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  generation_id TEXT NOT NULL,
  storage_key TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  mime_type TEXT NOT NULL DEFAULT 'image/png',
  filename TEXT NOT NULL,
  byte_size INTEGER NOT NULL,
  width INTEGER NOT NULL,
  height INTEGER NOT NULL,
  revised_prompt TEXT,
  output_index INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'ready',
  created_at TEXT NOT NULL,
  FOREIGN KEY (agent_id, message_id) REFERENCES agent_messages(agent_id, id) ON DELETE CASCADE,
  UNIQUE(message_id, generation_id, output_index),
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(generation_id AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'),
  CHECK (storage_key = 'objects/' || substr(sha256, 1, 2) || '/' || sha256 || '.png'),
  CHECK (mime_type = 'image/png'),
  CHECK (length(CAST(filename AS BLOB)) BETWEEN 1 AND 255),
  CHECK (byte_size BETWEEN 1 AND 10485760),
  CHECK (width BETWEEN 1 AND 8192),
  CHECK (height BETWEEN 1 AND 8192),
  CHECK (width * height <= 32000000),
  CHECK (revised_prompt IS NULL OR length(CAST(revised_prompt AS BLOB)) <= 131072),
  CHECK (output_index >= 0),
  CHECK (status IN ('ready', 'unavailable'))
);
CREATE INDEX IF NOT EXISTS idx_generated_images_message ON agent_message_generated_images(message_id, output_index, id);
CREATE INDEX IF NOT EXISTS idx_generated_images_agent_created ON agent_message_generated_images(agent_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_generated_images_storage_key ON agent_message_generated_images(storage_key);
CREATE INDEX IF NOT EXISTS idx_generated_images_run ON agent_message_generated_images(run_id);
CREATE INDEX IF NOT EXISTS idx_generated_images_agent_message ON agent_message_generated_images(agent_id, message_id);
`

const automationAuditSchemaSQL = `

CREATE TABLE IF NOT EXISTS automation_audit_events (
  id TEXT PRIMARY KEY,
  category TEXT NOT NULL,
  action TEXT NOT NULL,
  actor TEXT NOT NULL,
  agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  subject_type TEXT,
  subject_id TEXT,
  outcome TEXT NOT NULL,
  risk TEXT NOT NULL,
  details_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  CHECK (outcome IN ('success', 'failure', 'denied', 'error', 'unknown')),
  CHECK (risk IN ('none', 'low', 'medium', 'high', 'critical'))
);
CREATE INDEX IF NOT EXISTS idx_automation_audit_created ON automation_audit_events(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_automation_audit_category_action ON automation_audit_events(category, action, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_automation_audit_agent ON automation_audit_events(agent_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_automation_audit_run ON automation_audit_events(run_id, created_at DESC, id DESC);
`

const integrationConnectionsSchemaSQL = `

CREATE TABLE IF NOT EXISTS integration_connections (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  endpoint TEXT NOT NULL DEFAULT '',
  settings_json TEXT NOT NULL DEFAULT '{}',
  secret_refs_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(kind, name),
  CHECK (enabled IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_integration_connections_enabled ON integration_connections(enabled);
CREATE INDEX IF NOT EXISTS idx_integration_connections_kind ON integration_connections(kind);
`

const pluginSchemaSQL = `

CREATE TABLE IF NOT EXISTS plugins (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL COLLATE NOCASE UNIQUE,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  manifest_version TEXT NOT NULL,
  root_path TEXT NOT NULL,
  command TEXT NOT NULL,
  args_json TEXT NOT NULL DEFAULT '[]',
  env_json TEXT NOT NULL DEFAULT '{}',
  secret_refs_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'unknown',
  revision INTEGER NOT NULL DEFAULT 1,
  manifest_hash TEXT NOT NULL,
  last_checked_at TEXT,
  last_error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (enabled IN (0, 1)),
  CHECK (revision >= 1),
  CHECK (length(CAST(slug AS BLOB)) BETWEEN 1 AND 63),
  CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 120),
  CHECK (length(CAST(version AS BLOB)) BETWEEN 1 AND 64),
  CHECK (length(CAST(description AS BLOB)) <= 1000),
  CHECK (length(CAST(manifest_version AS BLOB)) BETWEEN 1 AND 64),
  CHECK (length(CAST(root_path AS BLOB)) BETWEEN 1 AND 4096),
  CHECK (length(CAST(command AS BLOB)) BETWEEN 1 AND 1024),
  CHECK (length(CAST(status AS BLOB)) BETWEEN 1 AND 64),
  CHECK (length(CAST(manifest_hash AS BLOB)) = 64),
  CHECK (last_error IS NULL OR length(CAST(last_error AS BLOB)) <= 4096),
  CHECK (json_valid(args_json) AND json_type(args_json) = 'array'),
  CHECK (json_valid(env_json) AND json_type(env_json) = 'object'),
  CHECK (json_valid(secret_refs_json) AND json_type(secret_refs_json) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_plugins_enabled_status ON plugins(enabled, status, slug, id);

CREATE TABLE IF NOT EXISTS plugin_tools (
  plugin_id TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
  remote_name TEXT NOT NULL,
  exposed_name TEXT NOT NULL COLLATE NOCASE UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  input_schema_json TEXT NOT NULL DEFAULT '{}',
  discovered_at TEXT NOT NULL,
  PRIMARY KEY (plugin_id, remote_name),
  CHECK (length(CAST(remote_name AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(exposed_name AS BLOB)) BETWEEN 1 AND 192),
  CHECK (length(CAST(description AS BLOB)) <= 2048),
  CHECK (length(CAST(input_schema_json AS BLOB)) <= 65536),
  CHECK (json_valid(input_schema_json) AND json_type(input_schema_json) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_plugin_tools_plugin_exposed ON plugin_tools(plugin_id, exposed_name COLLATE NOCASE);
`

const backgroundTaskSchemaSQL = `

CREATE TABLE IF NOT EXISTS background_tasks (
  id TEXT PRIMARY KEY,
  owner_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  parent_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  parent_tool_use_id TEXT,
  kind TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  revision INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 0,
  payload_json TEXT NOT NULL DEFAULT '{}',
  public_summary_json TEXT NOT NULL DEFAULT '{}',
  permission_mode_cap TEXT NOT NULL DEFAULT '',
  permission_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  policy_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  agent_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  tool_catalog_digest TEXT NOT NULL DEFAULT '',
  workspace_fingerprint TEXT NOT NULL DEFAULT '',
  child_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  child_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  resume_parent INTEGER NOT NULL DEFAULT 0,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 1,
  worker_instance_id TEXT,
  cancel_requested_at TEXT,
  started_at TEXT,
  completed_at TEXT,
  result_json TEXT NOT NULL DEFAULT '{}',
  error_code TEXT,
  error_message TEXT,
  exit_code INTEGER,
  last_output_sequence INTEGER NOT NULL DEFAULT 0,
  output_bytes INTEGER NOT NULL DEFAULT 0,
  output_truncated INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (kind IN ('shell', 'agent')),
  CHECK (status IN ('queued', 'waiting_approval', 'running', 'cancel_requested', 'succeeded', 'failed', 'canceled', 'interrupted')),
  CHECK (revision >= 1),
  CHECK (priority BETWEEN -1000000 AND 1000000),
  CHECK (json_valid(payload_json) AND json_type(payload_json) = 'object'),
  CHECK (json_valid(public_summary_json) AND json_type(public_summary_json) = 'object'),
  CHECK (json_valid(result_json) AND json_type(result_json) IN ('object', 'array')),
  CHECK (length(CAST(payload_json AS BLOB)) <= 262144),
  CHECK (length(CAST(public_summary_json AS BLOB)) <= 32768),
  CHECK (length(CAST(result_json AS BLOB)) <= 32768),
  CHECK (permission_mode_cap IN ('', 'readOnly', 'acceptEdits', 'bypassPermissions')),
  CHECK (permission_generation_snapshot >= 0),
  CHECK (policy_generation_snapshot >= 0),
  CHECK (agent_generation_snapshot >= 0),
  CHECK (resume_parent IN (0, 1)),
  CHECK (attempt_count >= 0),
  CHECK (max_attempts BETWEEN 1 AND 100),
  CHECK (last_output_sequence >= 0),
  CHECK (output_bytes >= 0),
  CHECK (output_truncated IN (0, 1)),
  CHECK (error_code IS NULL OR length(CAST(error_code AS BLOB)) <= 128),
  CHECK (error_message IS NULL OR length(CAST(error_message AS BLOB)) <= 4096),
  CHECK (length(CAST(tool_catalog_digest AS BLOB)) <= 512),
  CHECK (length(CAST(workspace_fingerprint AS BLOB)) <= 512)
);
CREATE INDEX IF NOT EXISTS idx_background_tasks_claim ON background_tasks(status, priority DESC, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_background_tasks_owner ON background_tasks(owner_agent_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_background_tasks_parent_run ON background_tasks(parent_run_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_background_tasks_worker ON background_tasks(worker_instance_id, status, updated_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_background_tasks_child_agent ON background_tasks(child_agent_id);
CREATE INDEX IF NOT EXISTS idx_background_tasks_child_run ON background_tasks(child_run_id);

CREATE TABLE IF NOT EXISTS background_task_output (
  task_id TEXT NOT NULL REFERENCES background_tasks(id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL,
  stream TEXT NOT NULL,
  chunk_blob BLOB NOT NULL,
  byte_count INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (task_id, sequence),
  CHECK (sequence >= 1),
  CHECK (stream IN ('stdout', 'stderr', 'system', 'truncated')),
  CHECK (byte_count >= 0),
  CHECK (length(chunk_blob) = byte_count),
  CHECK (byte_count <= 16384)
);
CREATE INDEX IF NOT EXISTS idx_background_task_output_page ON background_task_output(task_id, sequence ASC);
`

const planSchemaSQL = `

CREATE TABLE IF NOT EXISTS plans (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  source_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  revision INTEGER NOT NULL DEFAULT 1,
  content_json TEXT NOT NULL DEFAULT '{}',
  summary TEXT NOT NULL DEFAULT '',
  policy_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  agent_generation_snapshot INTEGER NOT NULL DEFAULT 0,
  tool_catalog_digest TEXT NOT NULL DEFAULT '',
  workspace_fingerprint TEXT NOT NULL DEFAULT '',
  stale_reason TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (status IN ('draft', 'in_review', 'approved', 'rejected', 'stale', 'executing', 'executed', 'cancelled')),
  CHECK (revision >= 1),
  CHECK (policy_generation_snapshot >= 0),
  CHECK (agent_generation_snapshot >= 0),
  CHECK (length(CAST(content_json AS BLOB)) BETWEEN 2 AND 262144),
  CHECK (json_valid(content_json) AND json_type(content_json) IN ('object', 'array')),
  CHECK (length(CAST(summary AS BLOB)) <= 4096),
  CHECK (length(CAST(tool_catalog_digest AS BLOB)) <= 512),
  CHECK (length(CAST(workspace_fingerprint AS BLOB)) <= 512),
  CHECK (stale_reason IS NULL OR length(CAST(stale_reason AS BLOB)) BETWEEN 1 AND 4096)
);
CREATE INDEX IF NOT EXISTS idx_plans_agent_updated ON plans(agent_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_plans_agent_status_updated ON plans(agent_id, status, updated_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_source_run ON plans(source_run_id) WHERE source_run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_plans_source_run_fk ON plans(source_run_id);

CREATE TABLE IF NOT EXISTS plan_reviews (
  id TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
  plan_revision INTEGER NOT NULL,
  reviewer_id TEXT NOT NULL,
  decision TEXT NOT NULL DEFAULT 'comment',
  comment TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  CHECK (plan_revision >= 1),
  CHECK (decision IN ('comment', 'approved', 'changes_requested')),
  CHECK (length(CAST(reviewer_id AS BLOB)) BETWEEN 1 AND 200),
  CHECK (length(CAST(comment AS BLOB)) <= 16384)
);
CREATE INDEX IF NOT EXISTS idx_plan_reviews_plan_created ON plan_reviews(plan_id, plan_revision, created_at ASC, id ASC);

CREATE TABLE IF NOT EXISTS plan_approvals (
  id TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
  plan_revision INTEGER NOT NULL,
  approver_id TEXT NOT NULL,
  decision TEXT NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(plan_id, plan_revision, approver_id),
  CHECK (plan_revision >= 1),
  CHECK (decision IN ('approved', 'rejected')),
  CHECK (length(CAST(approver_id AS BLOB)) BETWEEN 1 AND 200),
  CHECK (length(CAST(comment AS BLOB)) <= 16384)
);
CREATE INDEX IF NOT EXISTS idx_plan_approvals_plan_created ON plan_approvals(plan_id, plan_revision, created_at ASC, id ASC);
`

const gatewaySchemaSQL = `

CREATE TABLE IF NOT EXISTS gateway_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  key_prefix TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  allowed_models_json TEXT NOT NULL DEFAULT '[]',
  requests_per_minute INTEGER NOT NULL DEFAULT 0,
  monthly_token_limit INTEGER NOT NULL DEFAULT 0,
  max_concurrency INTEGER NOT NULL DEFAULT 0,
  expires_at TEXT,
  last_used_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (length(CAST(id AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 120),
  CHECK (length(CAST(key_prefix AS BLOB)) BETWEEN 1 AND 32),
  CHECK (key_prefix GLOB '[A-Za-z0-9]*'),
  CHECK (key_prefix NOT GLOB '*[^A-Za-z0-9._-]*'),
  CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
  CHECK (enabled IN (0, 1)),
  CHECK (length(CAST(allowed_models_json AS BLOB)) <= 32768),
  CHECK (json_valid(allowed_models_json) AND json_type(allowed_models_json) = 'array'),
  CHECK (requests_per_minute >= 0),
  CHECK (monthly_token_limit >= 0),
  CHECK (max_concurrency >= 0),
  CHECK (revoked_at IS NULL OR enabled = 0)
);
CREATE INDEX IF NOT EXISTS idx_gateway_keys_enabled ON gateway_keys(enabled, revoked_at, expires_at, created_at, id);
CREATE INDEX IF NOT EXISTS idx_gateway_keys_prefix ON gateway_keys(key_prefix, id);

CREATE TABLE IF NOT EXISTS gateway_models (
  alias TEXT PRIMARY KEY,
  target_model TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (length(CAST(alias AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(target_model AS BLOB)) BETWEEN 1 AND 256),
  CHECK (enabled IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_gateway_models_enabled_alias ON gateway_models(enabled, alias);
`

const gatewayAccountGrantsSchemaSQL = `

CREATE TABLE IF NOT EXISTS gateway_account_grants (
  provider TEXT NOT NULL,
  account_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider, account_id),
  CHECK (length(CAST(provider AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(account_id AS BLOB)) BETWEEN 1 AND 128)
);
`

const memorySchemaSQL = `

-- agent_id scopes a memory to one conversation. NULL means global: available to
-- every agent through keyword matching. A scoped memory dies with its
-- conversation, which is why the reference cascades.
CREATE TABLE IF NOT EXISTS memories (
  id TEXT PRIMARY KEY,
  agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  keywords_json TEXT NOT NULL DEFAULT '[]',
  pinned INTEGER NOT NULL DEFAULT 0,
  archived_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (pinned IN (0, 1)),
  CHECK (length(CAST(content AS BLOB)) BETWEEN 1 AND 16384)
);
CREATE INDEX IF NOT EXISTS idx_memories_pinned_updated ON memories(pinned DESC, updated_at DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_memories_archived ON memories(archived_at, updated_at DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_memories_agent ON memories(agent_id, pinned DESC, updated_at DESC, id ASC);

CREATE TABLE IF NOT EXISTS memory_injections (
  memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  injected_at TEXT NOT NULL,
  PRIMARY KEY (memory_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_memory_injections_agent ON memory_injections(agent_id, injected_at DESC, memory_id);
`

const schedulesSchemaSQL = `

CREATE TABLE IF NOT EXISTS schedules (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  expression TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  prompt TEXT NOT NULL,
  permission_mode TEXT NOT NULL,
  environment_mode TEXT NOT NULL DEFAULT 'workline',
  narrator_mode TEXT NOT NULL DEFAULT 'reuse',
  enabled INTEGER NOT NULL DEFAULT 1,
  next_run_at TEXT,
  last_run_at TEXT,
  last_run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  last_outcome TEXT,
  last_error TEXT,
  lease_until TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (permission_mode IN ('readOnly', 'acceptEdits')),
  CHECK (environment_mode IN ('workline', 'standalone')),
  CHECK (narrator_mode IN ('reuse', 'new')),
  CHECK (enabled IN (0, 1)),
  CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 120),
  CHECK (length(CAST(expression AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(timezone AS BLOB)) BETWEEN 1 AND 128),
  CHECK (length(CAST(prompt AS BLOB)) BETWEEN 1 AND 131072),
  CHECK (last_outcome IS NULL OR last_outcome IN ('success', 'failure', 'skipped', 'error'))
);
CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(enabled, next_run_at, lease_until, id);
CREATE INDEX IF NOT EXISTS idx_schedules_agent ON schedules(agent_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_schedules_last_run ON schedules(last_run_id);
`

const notificationDeliveriesSchemaSQL = `

CREATE TABLE IF NOT EXISTS notification_deliveries (
  id TEXT PRIMARY KEY,
  dedupe_key TEXT NOT NULL UNIQUE,
  sink_type TEXT NOT NULL,
  sink_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  tool_use_id TEXT,
  execution_generation INTEGER NOT NULL DEFAULT 0,
  payload_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'queued',
  attempt_count INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  next_attempt_at TEXT NOT NULL,
  lease_until TEXT,
  last_http_status INTEGER,
  last_error TEXT,
  delivered_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (sink_type IN ('webhook', 'telegram')),
  CHECK (status IN ('queued', 'inflight', 'retry_wait', 'delivered', 'dead')),
  CHECK (execution_generation >= 0),
  CHECK (attempt_count >= 0),
  CHECK (max_attempts BETWEEN 1 AND 100),
  CHECK (last_http_status IS NULL OR last_http_status BETWEEN 100 AND 599),
  CHECK (length(CAST(dedupe_key AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(payload_json AS BLOB)) BETWEEN 2 AND 32768)
);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_claim ON notification_deliveries(status, next_attempt_at, lease_until, id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_event ON notification_deliveries(event_type, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_agent ON notification_deliveries(agent_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_run ON notification_deliveries(run_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_generation ON notification_deliveries(agent_id, execution_generation, created_at, id);
`

const channelPersistenceSchemaSQL = `

CREATE TABLE IF NOT EXISTS channel_pairings (
  id TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL REFERENCES integration_connections(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending',
  code_hash TEXT,
  expires_at TEXT,
  chat_id TEXT,
  user_id TEXT,
  failed_attempts INTEGER NOT NULL DEFAULT 0,
  locked_until TEXT,
  credential_revision INTEGER NOT NULL DEFAULT 0,
  paired_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (status IN ('pending', 'active', 'revoked')),
  CHECK (failed_attempts >= 0),
  CHECK (credential_revision >= 0),
  CHECK (
    (status = 'pending' AND length(COALESCE(code_hash, '')) BETWEEN 32 AND 256 AND expires_at IS NOT NULL AND paired_at IS NULL AND revoked_at IS NULL)
    OR (status = 'active' AND code_hash IS NULL AND chat_id IS NOT NULL AND user_id IS NOT NULL AND paired_at IS NOT NULL AND revoked_at IS NULL)
    OR (status = 'revoked' AND code_hash IS NULL AND revoked_at IS NOT NULL)
  )
);
CREATE INDEX IF NOT EXISTS idx_channel_pairings_connection_status ON channel_pairings(connection_id, status, updated_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_channel_pairings_agent_status ON channel_pairings(agent_id, status, updated_at DESC, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_pairings_active_identity ON channel_pairings(connection_id, chat_id, user_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS channel_events (
  id TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL REFERENCES integration_connections(id) ON DELETE CASCADE,
  external_event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  run_id TEXT REFERENCES runs(id) ON DELETE SET NULL,
  tool_use_id TEXT,
  chat_id TEXT,
  user_id TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT,
  processed_at TEXT,
  created_at TEXT NOT NULL,
  UNIQUE(connection_id, external_event_id),
  CHECK (length(CAST(external_event_id AS BLOB)) BETWEEN 1 AND 256),
  CHECK (length(CAST(event_type AS BLOB)) BETWEEN 1 AND 96),
  CHECK (length(CAST(payload_json AS BLOB)) BETWEEN 2 AND 32768)
);
CREATE INDEX IF NOT EXISTS idx_channel_events_connection_created ON channel_events(connection_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_channel_events_agent_created ON channel_events(agent_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_channel_events_unprocessed ON channel_events(connection_id, processed_at, created_at, id);

CREATE TABLE IF NOT EXISTS channel_cursors (
  connection_id TEXT PRIMARY KEY REFERENCES integration_connections(id) ON DELETE CASCADE,
  offset INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  CHECK (offset >= 0)
);
`

const deviceActionRequestsSchemaSQL = `

CREATE TABLE IF NOT EXISTS device_action_requests (
  id TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL REFERENCES integration_connections(id) ON DELETE CASCADE,
  entity_id TEXT NOT NULL,
  domain TEXT NOT NULL,
  service TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  risk TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  requested_by TEXT NOT NULL,
  approved_by TEXT,
  expires_at TEXT NOT NULL,
  last_error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  CHECK (risk IN ('low', 'medium', 'high', 'critical')),
  CHECK (status IN ('pending', 'approved', 'denied', 'executing', 'succeeded', 'failed', 'expired')),
  CHECK (length(CAST(payload_json AS BLOB)) BETWEEN 2 AND 32768),
  CHECK (
    (status = 'pending' AND approved_by IS NULL AND completed_at IS NULL)
    OR (status IN ('approved', 'executing') AND approved_by IS NOT NULL AND completed_at IS NULL)
    OR (status IN ('denied', 'succeeded', 'failed', 'expired') AND completed_at IS NOT NULL)
  )
);
CREATE INDEX IF NOT EXISTS idx_device_action_requests_status ON device_action_requests(status, expires_at, created_at, id);
CREATE INDEX IF NOT EXISTS idx_device_action_requests_connection ON device_action_requests(connection_id, created_at DESC, id DESC);
`

const specSchemaSQL = `

CREATE TABLE IF NOT EXISTS spec_boards (
  agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  CHECK (revision >= 0)
);

CREATE TABLE IF NOT EXISTS spec_tasks (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  text TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'todo',
  protected INTEGER NOT NULL DEFAULT 0,
  position INTEGER NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1,
  source_type TEXT NOT NULL DEFAULT 'manual',
  source_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(agent_id, position),
  CHECK (status IN ('todo', 'doing', 'done', 'blocked')),
  CHECK (protected IN (0, 1)),
  CHECK (position >= 0),
  CHECK (revision >= 1),
  CHECK (source_type IN ('manual', 'goal', 'automation', 'migration', 'system')),
  CHECK (length(CAST(text AS BLOB)) BETWEEN 1 AND 16384),
  CHECK (length(CAST(source_type AS BLOB)) BETWEEN 1 AND 32),
  CHECK (source_id IS NULL OR length(CAST(source_id AS BLOB)) BETWEEN 1 AND 256)
);
CREATE INDEX IF NOT EXISTS idx_spec_tasks_agent_position ON spec_tasks(agent_id, position, id);
CREATE INDEX IF NOT EXISTS idx_spec_tasks_agent_status ON spec_tasks(agent_id, status, position, id);

CREATE TABLE IF NOT EXISTS goal_confirmations (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  task_id TEXT NOT NULL REFERENCES spec_tasks(id) ON DELETE CASCADE,
  queue_state TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'confirmed',
  created_at TEXT NOT NULL,
  CHECK (queue_state IN ('idle', 'busy')),
  CHECK (status IN ('confirmed', 'superseded'))
);
CREATE INDEX IF NOT EXISTS idx_goal_confirmations_agent_created ON goal_confirmations(agent_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_goal_confirmations_task ON goal_confirmations(task_id, created_at DESC, id DESC);
`

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
