package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/sessionstate"
)

type dbRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) ensureSchema(ctx context.Context) error {
	// These two lists bake the session status and driver enums into CHECK
	// constraints. This is persistence, not domain policy leaking downward: the
	// constraint is the schema's own integrity guarantee, and generating it from
	// the canonical enum is what keeps the DDL from drifting out of sync with the
	// Go definition. It is also the backstop that keeps CreateSession rejecting a
	// bogus status now that the explicit pre-check moved to the caller.
	statusCheck := sqlStringList(sessionstate.AllStatuses())
	driverCheck := sqlStringList([]string{string(agents.Pi), string(agents.Shell)})
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('user','admin')),
  created_at TEXT NOT NULL
);

-- R0 platform foundation: mutable operational switches are data, not build
-- time environment. Keys are intentionally opaque so future releases can add
-- flags without another schema migration.
CREATE TABLE IF NOT EXISTS feature_flags (
  key TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  description TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS admin_audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_user_id TEXT NOT NULL,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id TEXT NOT NULL DEFAULT '',
  before_json TEXT NOT NULL DEFAULT '',
  after_json TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS admin_audit_log_created_idx ON admin_audit_log(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS admin_audit_log_actor_idx ON admin_audit_log(actor_user_id, created_at DESC);

-- Cron Agent schedules are control-plane state. Each execution gets its own
-- durable run row, session and sandbox generation; no scheduler state is kept
-- only in memory, so restarts can safely resume due work.
CREATE TABLE IF NOT EXISTS agent_schedules (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  name TEXT NOT NULL,
  prompt TEXT NOT NULL,
  cron_expr TEXT NOT NULL DEFAULT '',
  timezone TEXT NOT NULL DEFAULT 'UTC',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
  mode TEXT NOT NULL DEFAULT 'agent' CHECK(mode IN ('agent')),
  archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
  result_mode TEXT NOT NULL DEFAULT 'new_session' CHECK(result_mode IN ('new_session','existing_session')),
  target_session_id TEXT,
  timeout_seconds INTEGER NOT NULL DEFAULT 1800 CHECK(timeout_seconds > 0),
  max_retries INTEGER NOT NULL DEFAULT 0 CHECK(max_retries >= 0 AND max_retries <= 10),
  retry_backoff_seconds INTEGER NOT NULL DEFAULT 30 CHECK(retry_backoff_seconds >= 0),
  next_run_at TEXT,
  last_run_at TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_schedules_due_idx ON agent_schedules(enabled, next_run_at);
CREATE INDEX IF NOT EXISTS agent_schedules_user_idx ON agent_schedules(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS schedule_runs (
  id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT,
  turn_id INTEGER,
  scheduled_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','timed_out','canceled')),
  attempt INTEGER NOT NULL DEFAULT 0,
  error_class TEXT,
  error TEXT,
  output_summary TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(schedule_id) REFERENCES agent_schedules(id) ON DELETE CASCADE,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE SET NULL,
  FOREIGN KEY(turn_id) REFERENCES turns(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS schedule_runs_schedule_idx ON schedule_runs(schedule_id, scheduled_at DESC);
CREATE INDEX IF NOT EXISTS schedule_runs_user_idx ON schedule_runs(user_id, scheduled_at DESC);
CREATE INDEX IF NOT EXISTS schedule_runs_status_idx ON schedule_runs(status, started_at);
CREATE UNIQUE INDEX IF NOT EXISTS schedule_runs_schedule_slot_uq ON schedule_runs(schedule_id, scheduled_at, attempt);

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  last_seen_at TEXT
);

CREATE INDEX IF NOT EXISTS auth_sessions_user_idx ON auth_sessions(user_id);
CREATE INDEX IF NOT EXISTS auth_sessions_expires_idx ON auth_sessions(expires_at);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN (`+statusCheck+`)),
  driver_id TEXT NOT NULL CHECK(driver_id IN (`+driverCheck+`)),
  mode TEXT NOT NULL CHECK(mode IN ('agent','shell')),
  workspace TEXT NOT NULL,
  restore_id TEXT NOT NULL,
  restore_ms INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT,
  ended_at TEXT,
  last_activity_at TEXT,
  checkpoint_path TEXT,
  auto_checkpoint_enabled INTEGER NOT NULL DEFAULT 0 CHECK(auto_checkpoint_enabled IN (0,1)),
  active_generation_id TEXT REFERENCES runtime_generations(generation_id),
  agent_home_path TEXT,
  failure_reason TEXT,
  error_class TEXT,
  title TEXT NOT NULL DEFAULT '',
  title_custom INTEGER NOT NULL DEFAULT 0 CHECK(title_custom IN (0,1))
);

CREATE INDEX IF NOT EXISTS sessions_status_idx ON sessions(status);
CREATE INDEX IF NOT EXISTS sessions_active_generation_idx ON sessions(active_generation_id);

CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL,
  stop_reason TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS messages_session_idx ON messages(session_id, id);

-- Full-text search over message content. We use an external-content FTS5 index
-- (content='messages') so the text is not stored twice: the vtable holds only
-- the inverted index and reads column values back from messages by rowid. The
-- trigram tokenizer gives reliable substring matching for any script (including
-- Chinese, which has no whitespace word boundaries); the trade-off is that a
-- MATCH token must be at least 3 characters, which the query layer handles.
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
  content,
  content='messages',
  content_rowid='id',
  tokenize='trigram'
);

-- External-content sync triggers: keep messages_fts consistent with messages.
-- The 'delete' command feeds the old content back so the index entry can be
-- removed; updates are a delete of the old row followed by an insert of the new.
CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
  INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TABLE IF NOT EXISTS artifacts (
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mod_time TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(session_id, path),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS orchestrator_owner (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  uuid TEXT NOT NULL,
  boot_id TEXT NOT NULL,
  host_run_dir TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS egress_policies (
  egress_policy_id TEXT PRIMARY KEY,
  policy_digest TEXT NOT NULL,
  allowed_egress_rules TEXT NOT NULL,
  doris_fe_hosts TEXT NOT NULL,
  doris_be_hosts TEXT NOT NULL,
  doris_ports TEXT NOT NULL,
  dns_policy TEXT NOT NULL CHECK(dns_policy IN ('off','hostnames_only','always')),
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_runtime_profiles (
  agent_runtime_profile_id TEXT PRIMARY KEY,
  driver_id TEXT NOT NULL CHECK(driver_id IN (`+driverCheck+`)),
  model TEXT,
  output_format TEXT NOT NULL,
  disable_nonessential_traffic INTEGER NOT NULL CHECK(disable_nonessential_traffic IN (0,1)),
  sandbox_uid INTEGER NOT NULL CHECK(sandbox_uid > 0),
  sandbox_gid INTEGER NOT NULL CHECK(sandbox_gid > 0),
  sandbox_supplemental_gids TEXT NOT NULL,
  requires_secret_drop INTEGER NOT NULL CHECK(requires_secret_drop IN (0,1)),
  model_access_allowed INTEGER NOT NULL CHECK(model_access_allowed IN (0,1)),
  manifest_model_proxy_base_url TEXT,
  model_proxy_api_key_secret_id TEXT,
  model_proxy_auth_token_secret_id TEXT,
  secret_version TEXT,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_runtime_profiles_tuple_uq
  ON agent_runtime_profiles (
    driver_id,
    COALESCE(model, ''),
    output_format,
    disable_nonessential_traffic,
    sandbox_uid,
    sandbox_gid,
    sandbox_supplemental_gids,
    requires_secret_drop,
    model_access_allowed,
    COALESCE(manifest_model_proxy_base_url, ''),
    COALESCE(model_proxy_api_key_secret_id, ''),
    COALESCE(model_proxy_auth_token_secret_id, ''),
    COALESCE(secret_version, '')
  );

CREATE TABLE IF NOT EXISTS runtime_generations (
  generation_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('allocating','starting','probing','active','idle','checkpointing','checkpointed','restoring','failed','destroyed')),
  checkpoint_created_at TEXT,
  checkpoint_network_profile_id TEXT,
  checkpoint_agent_runtime_profile_id TEXT,
  checkpoint_runsc_version TEXT,
  checkpoint_runsc_platform TEXT,
  checkpoint_runsc_binary_path TEXT,
  checkpoint_runsc_binary_digest TEXT,
  checkpoint_bundle_digest TEXT,
  checkpoint_runtime_config_digest TEXT,
  checkpoint_control_manifest_digest TEXT,
  checkpoint_driver_states_digest TEXT,
  network_profile_id TEXT,
  agent_runtime_profile_id TEXT,
  runsc_platform TEXT DEFAULT 'systrap',
  runsc_version TEXT,
  sandbox_contract_id TEXT REFERENCES sandbox_contracts(contract_id),
  sandbox_contract_version TEXT,
  auto_checkpoint_enabled INTEGER NOT NULL DEFAULT 0 CHECK(auto_checkpoint_enabled IN (0,1)),
  lease_owner TEXT,
  lease_expires_at TEXT,
  started_at TEXT,
  last_seen_at TEXT,
  -- sandbox_last_seen_at is written ONLY by the sandbox-driven heartbeat path
  -- (RenewGenerationHeartbeat). Unlike last_seen_at, no orchestrator-side lease
  -- renewal touches it, so a stale value is a reliable signal that the sandbox
  -- process itself has stopped reporting (see RenewLiveGenerationLeases gate).
  sandbox_last_seen_at TEXT,
  ended_at TEXT,
  failure_reason TEXT,
  error_class TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(checkpoint_network_profile_id) REFERENCES network_profiles(network_profile_id) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(checkpoint_agent_runtime_profile_id) REFERENCES agent_runtime_profiles(agent_runtime_profile_id) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(network_profile_id) REFERENCES network_profiles(network_profile_id) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(agent_runtime_profile_id) REFERENCES agent_runtime_profiles(agent_runtime_profile_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generations_one_nonterminal_per_session
  ON runtime_generations (session_id)
  WHERE status NOT IN ('failed', 'destroyed');

CREATE INDEX IF NOT EXISTS runtime_generations_session_status_idx
  ON runtime_generations (session_id, status);

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generations_sandbox_contract_id_uq
  ON runtime_generations (sandbox_contract_id)
  WHERE sandbox_contract_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS network_profiles (
  network_profile_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  runsc_network TEXT NOT NULL DEFAULT 'sandbox',
  runsc_overlay2 TEXT NOT NULL DEFAULT 'none',
  host_proxy_bind_url TEXT NOT NULL,
  proxy_port INTEGER NOT NULL,
  host_gateway_ip TEXT NOT NULL,
  sandbox_base_url TEXT NOT NULL,
  probe_url TEXT NOT NULL,
  netns_name TEXT NOT NULL,
  netns_path TEXT NOT NULL,
  host_veth TEXT NOT NULL,
  sandbox_veth TEXT NOT NULL,
  sandbox_ip_cidr TEXT NOT NULL,
  egress_policy_id TEXT NOT NULL,
  allowed_egress_rules TEXT NOT NULL,
  doris_fe_hosts TEXT NOT NULL,
  doris_be_hosts TEXT NOT NULL,
  doris_ports TEXT NOT NULL,
  dns_policy TEXT NOT NULL CHECK(dns_policy IN ('off','hostnames_only','always')),
  host_side_cidr TEXT NOT NULL,
  allocation_state TEXT NOT NULL CHECK(allocation_state IN ('allocating','ready','live','reserved_checkpointed','recreating','reclaimable','destroyed')),
  created_at TEXT NOT NULL,
  destroyed_at TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(egress_policy_id) REFERENCES egress_policies(egress_policy_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_netns_name_non_destroyed_uq
  ON network_profiles (netns_name)
  WHERE allocation_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_netns_path_non_destroyed_uq
  ON network_profiles (netns_path)
  WHERE allocation_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_host_veth_non_destroyed_uq
  ON network_profiles (host_veth)
  WHERE allocation_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_sandbox_veth_non_destroyed_uq
  ON network_profiles (sandbox_veth)
  WHERE allocation_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_host_gateway_ip_non_destroyed_uq
  ON network_profiles (host_gateway_ip)
  WHERE allocation_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_host_side_cidr_non_destroyed_uq
  ON network_profiles (host_side_cidr)
  WHERE allocation_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS network_profiles_sandbox_ip_cidr_non_destroyed_uq
  ON network_profiles (sandbox_ip_cidr)
  WHERE allocation_state != 'destroyed';

CREATE TABLE IF NOT EXISTS runtime_generation_resources (
  generation_id TEXT PRIMARY KEY,
  network_profile_id TEXT NOT NULL,
  agent_runtime_profile_id TEXT NOT NULL,
  control_dir_path TEXT NOT NULL,
  control_manifest_path TEXT NOT NULL,
  control_manifest_digest TEXT,
  projected_control_manifest_digest TEXT,
  bundle_digest TEXT,
  runtime_config_digest TEXT,
  spec_digest TEXT,
  bundle_dir_path TEXT NOT NULL,
  spec_path TEXT NOT NULL,
  checkpoint_path TEXT,
  secrets_dir_path TEXT,
  bridge_dir_path TEXT NOT NULL,
  log_dir_path TEXT NOT NULL,
  contract_id TEXT REFERENCES sandbox_contracts(contract_id),
  sandbox_contract_version TEXT,
  runsc_container_id TEXT,
  runsc_pid INTEGER,
  runsc_platform TEXT,
  runsc_version TEXT,
  runsc_binary_path TEXT,
  runsc_binary_digest TEXT,
  sandbox_ip TEXT,
  network_hosts_path TEXT,
  resource_identity_payload TEXT,
  resource_identity_digest TEXT,
  resource_state TEXT NOT NULL CHECK(resource_state IN ('allocating','ready','live','reserved_checkpointed','recreating','reclaimable','destroyed')),
  created_at TEXT NOT NULL,
  destroyed_at TEXT,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id) ON DELETE CASCADE,
  FOREIGN KEY(network_profile_id) REFERENCES network_profiles(network_profile_id) DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(agent_runtime_profile_id) REFERENCES agent_runtime_profiles(agent_runtime_profile_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_control_dir_path_non_destroyed_uq
  ON runtime_generation_resources (control_dir_path)
  WHERE resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_control_manifest_path_non_destroyed_uq
  ON runtime_generation_resources (control_manifest_path)
  WHERE resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_bundle_dir_path_non_destroyed_uq
  ON runtime_generation_resources (bundle_dir_path)
  WHERE resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_spec_path_non_destroyed_uq
  ON runtime_generation_resources (spec_path)
  WHERE resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_checkpoint_path_non_destroyed_uq
  ON runtime_generation_resources (checkpoint_path)
  WHERE checkpoint_path IS NOT NULL AND resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_bridge_dir_path_non_destroyed_uq
  ON runtime_generation_resources (bridge_dir_path)
  WHERE resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_log_dir_path_non_destroyed_uq
  ON runtime_generation_resources (log_dir_path)
  WHERE resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_secrets_dir_path_non_destroyed_uq
  ON runtime_generation_resources (secrets_dir_path)
  WHERE secrets_dir_path IS NOT NULL AND resource_state != 'destroyed';

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_contract_id_uq
  ON runtime_generation_resources (contract_id)
  WHERE contract_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS runtime_generation_resources_runsc_container_id_non_destroyed_uq
  ON runtime_generation_resources (runsc_container_id)
  WHERE runsc_container_id IS NOT NULL
    AND resource_state != 'destroyed';

CREATE TABLE IF NOT EXISTS turns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  role TEXT NOT NULL CHECK(role = 'user'),
  content TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('queued','leased','running','completed','failed','canceled')),
  generation_id TEXT,
  lease_owner TEXT,
  lease_expires_at TEXT,
  claim_request_id TEXT,
  claim_granted_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0,
  ack_started_at TEXT,
  completed_by_generation TEXT,
  retry_policy TEXT,
  created_at TEXT NOT NULL,
  started_at TEXT,
  completed_at TEXT,
  error_class TEXT,
  error TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id),
  FOREIGN KEY(completed_by_generation) REFERENCES runtime_generations(generation_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS turns_claim_request_id_uq
  ON turns (session_id, claim_request_id)
  WHERE claim_request_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS turns_session_sequence_uq
  ON turns (session_id, sequence);

CREATE INDEX IF NOT EXISTS turns_session_status_sequence_idx
  ON turns (session_id, status, sequence);

CREATE TABLE IF NOT EXISTS events (
  event_id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT,
  turn_id INTEGER,
  generation_id TEXT,
  output_sequence INTEGER,
  dedupe_key TEXT,
  proxy_request_id TEXT,
  stream TEXT,
  severity TEXT,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(turn_id) REFERENCES turns(id),
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS events_output_dedupe_uq
  ON events (turn_id, generation_id, output_sequence)
  WHERE output_sequence IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS events_dedupe_key_uq
  ON events (session_id, dedupe_key)
  WHERE dedupe_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS events_session_event_id_idx
  ON events (session_id, event_id);

CREATE INDEX IF NOT EXISTS events_proxy_request_id_idx
  ON events (proxy_request_id);

CREATE UNIQUE INDEX IF NOT EXISTS events_proxy_started_request_uq
  ON events (proxy_request_id)
  WHERE proxy_request_id IS NOT NULL
    AND type = 'proxy.request.started';

CREATE UNIQUE INDEX IF NOT EXISTS events_proxy_finished_request_uq
  ON events (proxy_request_id)
  WHERE proxy_request_id IS NOT NULL
    AND type IN ('proxy.request.completed', 'proxy.request.failed');

CREATE INDEX IF NOT EXISTS events_created_at_idx
  ON events (created_at);

CREATE TABLE IF NOT EXISTS active_model_request_contexts (
  sandbox_source_ip TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  turn_id INTEGER NOT NULL,
  lease_owner TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  next_request_sequence INTEGER NOT NULL,
  model_access_allowed INTEGER NOT NULL DEFAULT 0 CHECK(model_access_allowed IN (0,1)),
  registered_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id),
  FOREIGN KEY(turn_id) REFERENCES turns(id)
);

CREATE INDEX IF NOT EXISTS active_model_request_contexts_session_generation_idx
  ON active_model_request_contexts (session_id, generation_id);

CREATE TABLE IF NOT EXISTS sandbox_contracts (
  contract_id TEXT PRIMARY KEY,
  generation_id TEXT NOT NULL UNIQUE,
  session_id TEXT NOT NULL,
  sandbox_contract_version TEXT NOT NULL CHECK(sandbox_contract_version = 'sandbox-isolation-v1'),
  contract_schema_version INTEGER NOT NULL CHECK(contract_schema_version = 2),
  contract_gate_version TEXT NOT NULL CHECK(contract_gate_version = 'driver_manifest_v1'),
  canonical_payload TEXT NOT NULL,
  sandbox_contract_digest TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS sandbox_contract_artifacts (
  contract_id TEXT PRIMARY KEY,
  sandbox_contract_digest TEXT NOT NULL,
  network_hosts_digest TEXT,
  control_manifest_digest TEXT NOT NULL,
  oci_spec_digest TEXT NOT NULL,
  bundle_digest TEXT NOT NULL,
  checkpoint_metadata_digest TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(contract_id) REFERENCES sandbox_contracts(contract_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS sandbox_contract_input_evidence (
  contract_id TEXT PRIMARY KEY,
  runtime_config_digest TEXT NOT NULL,
  runtime_config_preimage TEXT NOT NULL,
  agent_manifest_digest TEXT NOT NULL,
  agent_manifest_payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(contract_id) REFERENCES sandbox_contracts(contract_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_workspaces (
  session_id TEXT PRIMARY KEY,
  host_path TEXT NOT NULL UNIQUE,
  layout_version INTEGER NOT NULL,
  sandbox_uid INTEGER NOT NULL,
  sandbox_gid INTEGER NOT NULL,
  sandbox_supplemental_gids TEXT NOT NULL,
  runtime_identity_digest TEXT NOT NULL,
  provisioned_at TEXT NOT NULL,
  provisioning_marker_path TEXT NOT NULL UNIQUE,
  provisioning_marker_digest TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_driver_homes (
  session_id TEXT NOT NULL,
  driver TEXT NOT NULL,
  host_path TEXT NOT NULL UNIQUE,
  layout_version INTEGER NOT NULL,
  sandbox_uid INTEGER NOT NULL,
  sandbox_gid INTEGER NOT NULL,
  sandbox_supplemental_gids TEXT NOT NULL,
  runtime_identity_digest TEXT NOT NULL,
  provisioned_at TEXT NOT NULL,
  provisioning_marker_path TEXT NOT NULL UNIQUE,
  provisioning_marker_digest TEXT NOT NULL,
  PRIMARY KEY(session_id, driver),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS session_driver_homes_session_idx
  ON session_driver_homes (session_id);

CREATE TABLE IF NOT EXISTS session_driver_states (
  session_id TEXT NOT NULL,
  driver_id TEXT NOT NULL CHECK(driver_id <> '' AND driver_id IN (`+driverCheck+`)),
  state_payload TEXT NOT NULL CHECK(state_payload <> ''),
  state_digest TEXT NOT NULL CHECK(state_digest LIKE 'sha256:%'),
  state_version INTEGER NOT NULL CHECK(state_version > 0),
  updated_generation_id TEXT NOT NULL,
  updated_turn_id INTEGER,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(session_id, driver_id),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(updated_generation_id) REFERENCES runtime_generations(generation_id) ON DELETE RESTRICT,
  FOREIGN KEY(updated_turn_id) REFERENCES turns(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS runtime_resource_instances (
  generation_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  contract_id TEXT NOT NULL,
  sandbox_contract_version TEXT NOT NULL CHECK(sandbox_contract_version = 'sandbox-isolation-v1'),
  worker_id TEXT,
  host_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('allocated','materializing','ready','live','checkpoint_reserved','retiring','reconciling','absent_verified','destroyed')),
  lease_expires_at TEXT,
  idempotency_token TEXT,
  runsc_container_id TEXT NOT NULL,
  runsc_platform TEXT NOT NULL,
  runsc_version TEXT NOT NULL,
  runsc_binary_path TEXT NOT NULL,
  runsc_binary_digest TEXT NOT NULL,
  network_profile_id TEXT NOT NULL,
  netns_name TEXT NOT NULL,
  netns_path TEXT NOT NULL,
  host_veth TEXT NOT NULL,
  sandbox_veth TEXT NOT NULL,
  host_gateway_ip TEXT NOT NULL,
  sandbox_ip TEXT NOT NULL,
  sandbox_ip_cidr TEXT NOT NULL,
  host_side_cidr TEXT NOT NULL,
  nft_table_name TEXT NOT NULL,
  control_dir_path TEXT NOT NULL,
  control_manifest_path TEXT NOT NULL,
  bundle_dir_path TEXT NOT NULL,
  spec_path TEXT NOT NULL,
  checkpoint_path TEXT,
  bridge_dir_path TEXT NOT NULL,
  network_hosts_path TEXT,
  log_dir_path TEXT NOT NULL,
  resource_identity_payload TEXT NOT NULL,
  resource_identity_digest TEXT NOT NULL,
  evidence_json TEXT,
  evidence_digest TEXT,
  verified_at TEXT,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id) ON DELETE CASCADE,
  FOREIGN KEY(contract_id) REFERENCES sandbox_contracts(contract_id),
  FOREIGN KEY(network_profile_id) REFERENCES network_profiles(network_profile_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_runsc_container_id_active_uq
  ON runtime_resource_instances (runsc_container_id)
  WHERE runsc_container_id IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_netns_name_active_uq
  ON runtime_resource_instances (netns_name)
  WHERE netns_name IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_netns_path_active_uq
  ON runtime_resource_instances (netns_path)
  WHERE netns_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_host_veth_active_uq
  ON runtime_resource_instances (host_veth)
  WHERE host_veth IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_sandbox_veth_active_uq
  ON runtime_resource_instances (sandbox_veth)
  WHERE sandbox_veth IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_host_gateway_ip_active_uq
  ON runtime_resource_instances (host_gateway_ip)
  WHERE host_gateway_ip IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_sandbox_ip_active_uq
  ON runtime_resource_instances (sandbox_ip)
  WHERE sandbox_ip IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_sandbox_ip_cidr_active_uq
  ON runtime_resource_instances (sandbox_ip_cidr)
  WHERE sandbox_ip_cidr IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_host_side_cidr_active_uq
  ON runtime_resource_instances (host_side_cidr)
  WHERE host_side_cidr IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_nft_table_name_active_uq
  ON runtime_resource_instances (nft_table_name)
  WHERE nft_table_name IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_control_dir_path_active_uq
  ON runtime_resource_instances (control_dir_path)
  WHERE control_dir_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_control_manifest_path_active_uq
  ON runtime_resource_instances (control_manifest_path)
  WHERE control_manifest_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_bundle_dir_path_active_uq
  ON runtime_resource_instances (bundle_dir_path)
  WHERE bundle_dir_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_spec_path_active_uq
  ON runtime_resource_instances (spec_path)
  WHERE spec_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_checkpoint_path_active_uq
  ON runtime_resource_instances (checkpoint_path)
  WHERE checkpoint_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_bridge_dir_path_active_uq
  ON runtime_resource_instances (bridge_dir_path)
  WHERE bridge_dir_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_network_hosts_path_active_uq
  ON runtime_resource_instances (network_hosts_path)
  WHERE network_hosts_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

CREATE UNIQUE INDEX IF NOT EXISTS runtime_resource_instances_log_dir_path_active_uq
  ON runtime_resource_instances (log_dir_path)
  WHERE log_dir_path IS NOT NULL
    AND state NOT IN ('absent_verified','destroyed');

-- user_id is the audience: NULL means broadcast to everyone (the original and
-- still the default for release notes), non-NULL means only that user sees it.
--
-- OBLIGATION: every read of this table must carry the audience predicate
-- "(user_id IS NULL OR user_id = ?)". There are two such reads --
-- ListNotificationsForUser and CountUnreadNotifications -- and adding the
-- predicate to only one of them yields an unread badge that disagrees with the
-- list it opens, which is worse than either being wrong on its own.
CREATE TABLE IF NOT EXISTS notifications (
  id TEXT PRIMARY KEY,
  user_id TEXT,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS notifications_created_at_idx
  ON notifications (created_at);

-- NOTE: the index on user_id is deliberately NOT here. On a database created
-- before the audience column existed, the CREATE TABLE above is a no-op, so the
-- column only appears once ensureNotificationColumns runs its ALTER -- which
-- happens after this whole DDL batch. Indexing user_id here would fail with
-- "no such column: user_id" on exactly those pre-existing databases, abort the
-- batch before the ALTER could fix it, and crash-loop the process on every
-- start. It is created in ensureNotificationColumns instead.

CREATE TABLE IF NOT EXISTS notification_reads (
  notification_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  read_at TEXT NOT NULL,
  PRIMARY KEY (notification_id, user_id),
  FOREIGN KEY(notification_id) REFERENCES notifications(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS notification_reads_user_idx
  ON notification_reads (user_id);

-- Execution trace nodes (thinking phases + tool calls) captured live during a
-- turn and attached to the assistant message they produced. Stored as a JSON
-- array so the shape can evolve without a migration; result payloads are kept
-- summarized/truncated by the writer, not archived whole.
CREATE TABLE IF NOT EXISTS message_traces (
  message_id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL,
  nodes TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS message_traces_session_idx
  ON message_traces (session_id);

-- Per-user thumbs up/down on an assistant message. One row per (message,user);
-- toggling the same rating off deletes the row.
CREATE TABLE IF NOT EXISTS message_feedback (
  message_id INTEGER NOT NULL,
  user_id TEXT NOT NULL,
  rating TEXT NOT NULL CHECK(rating IN ('up','down')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(message_id, user_id),
  FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS message_feedback_message_idx
  ON message_feedback (message_id);

-- Read-only sharing of a session with a named, logged-in user. role is an enum
-- with one member today so widening it later ('commenter', 'writer') is an
-- ALTER-free CHECK change rather than a column addition. grantee_user_id is a
-- username string, not a stable id: see the OBLIGATION on username reuse below.
CREATE TABLE IF NOT EXISTS session_shares (
  id               TEXT PRIMARY KEY,
  session_id       TEXT NOT NULL,
  grantee_user_id  TEXT NOT NULL,
  role             TEXT NOT NULL CHECK(role IN ('reader')),
  visibility       TEXT NOT NULL CHECK(visibility IN ('summary','full')),
  created_by       TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  revoked_at       TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

-- Uniqueness constrains only LIVE grants; without the partial predicate,
-- revoking a share and re-sharing with the same person would collide.
CREATE UNIQUE INDEX IF NOT EXISTS session_shares_live_idx
  ON session_shares (session_id, grantee_user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS session_shares_grantee_idx ON session_shares (grantee_user_id);

CREATE TABLE IF NOT EXISTS share_links (
  id               TEXT PRIMARY KEY,
  token_hash       TEXT NOT NULL UNIQUE,
  session_id       TEXT NOT NULL,
  visibility       TEXT NOT NULL CHECK(visibility IN ('summary','full')),
  max_message_id   INTEGER,
  created_by       TEXT NOT NULL,
  created_at       TEXT NOT NULL,
  expires_at       TEXT NOT NULL,
  revoked_at       TEXT,
  last_accessed_at TEXT,
  access_count     INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS share_links_session_idx ON share_links (session_id);

-- The file whitelist a public link exposes. Each row freezes the blob that was
-- current when the link was created; an empty set means "session text only".
CREATE TABLE IF NOT EXISTS share_link_files (
  link_id      TEXT NOT NULL,
  path         TEXT NOT NULL,
  blob_sha256  TEXT NOT NULL,
  size         INTEGER NOT NULL,
  PRIMARY KEY(link_id, path),
  FOREIGN KEY(link_id) REFERENCES share_links(id) ON DELETE CASCADE
);

-- ============================================================================
-- OBLIGATION: share reclamation is NOT automatic.
--
-- 1. Blob bytes live OUTSIDE SQLite. share_link_files.blob_sha256 points at a
--    content-addressed file in the share blob store; ON DELETE CASCADE removes
--    the table row and nothing on disk. Reclamation runs only through the
--    explicit path: SweepExpiredShareLinks returns the blob digests that just
--    lost their last reference, and the maintenance loop unlinks them. The
--    reference check and the delete MUST stay inside one write transaction --
--    a concurrent CreateShareLink that hashes to the same digest would
--    otherwise have its file deleted out from under it.
--
-- 2. The two sessions(id) ON DELETE CASCADE constraints above never fire in
--    this repository: session destruction is a soft delete (status flips to
--    'destroyed'). They are the backstop for a future hard delete, not the
--    mechanism this feature relies on.
--
-- 3. Identity is a username string with no stable id and no FK (the users table
--    holds only the built-in lab row). Deleting a user and recreating the same
--    name would hand the new person the old one's grants, so the
--    "orchestrator user rm" command MUST call RevokeSharesForUser -- and must
--    fail loudly if it cannot, because unlike a login cookie a share never
--    expires on its own.
-- ============================================================================

-- ============================================================================
-- MOVED OUT: trajectory distillation (agents-kb pipeline)
--
-- trajectory_snapshots, trajectory_events, extraction_state, episode_memories,
-- semantic_memories, skill_candidates, raw_sources, evolution_runs and
-- agent_calls used to be declared here. They now live in a physically separate
-- database owned by internal/memorystore, so an LLM distillation sweep can no
-- longer hold this database's single write lock or its connection pool.
--
-- No statement in this schema references those tables and no statement in that
-- one references anything here. SQLite cannot express a foreign key across
-- database files, so the five ON DELETE CASCADE constraints those tables held
-- against sessions(id) are gone. See the cross-database-reference note at the
-- top of internal/memorystore/schema.go for what that costs and the obligation
-- it places on any future session-purge feature.
-- ============================================================================
`)
	if err != nil {
		return err
	}
	if err := s.ensureSessionColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureScheduleColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureUserColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureFeatureFlagDefaults(ctx); err != nil {
		return err
	}
	if err := s.ensureMessageColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureNotificationColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureRuntimeGenerationColumns(ctx); err != nil {
		return err
	}
	return s.backfillMessagesFTS(ctx)
}

// ensureScheduleColumns upgrades Cron Agent rows created before result routing
// and soft-archive support. Constant defaults keep this metadata-only and make
// the migration safe to run during a mixed-version rollout.
func (s *Store) ensureScheduleColumns(ctx context.Context) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"archived", `ALTER TABLE agent_schedules ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`},
		{"result_mode", `ALTER TABLE agent_schedules ADD COLUMN result_mode TEXT NOT NULL DEFAULT 'new_session'`},
		{"target_session_id", `ALTER TABLE agent_schedules ADD COLUMN target_session_id TEXT`},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, "agent_schedules", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureFeatureFlagDefaults(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	defaults := []struct {
		key, description string
		enabled          bool
	}{
		{"admin_console", "Enable administrator console APIs and UI", false},
		{"cron_agent", "Enable Cron Agent features", true},
		{"skill_store", "Enable Skill Store features", false},
		{"evolution_admin", "Enable semantic-layer evolution administration", false},
	}
	for _, flag := range defaults {
		enabled := 0
		if flag.enabled {
			enabled = 1
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO feature_flags (key, enabled, description, updated_at, updated_by) VALUES (?, ?, ?, ?, '') ON CONFLICT(key) DO NOTHING`, flag.key, enabled, flag.description, now); err != nil {
			return err
		}
	}
	return nil
}

// ensureRuntimeGenerationColumns adds sandbox_last_seen_at (introduced to
// distinguish sandbox-driven heartbeats from orchestrator-side lease renewal)
// to existing DBs, and backfills started_at as a stable cold-start anchor.
//
// Same O(1) metadata-only ALTER + idempotency guarantee as ensureSessionColumns.
//
// The started_at backfill exists because started_at was declared in the
// original schema but never written until the sandbox-liveness fix began
// populating it in MarkGenerationResourcesLive. Live/idle generations that
// predate that change have NULL started_at; the liveness gate uses
// COALESCE(started_at, last_seen_at) as the cold-start grace anchor for rows
// whose sandbox_last_seen_at is still NULL. Seeding started_at from the
// already-recent last_seen_at for non-terminal rows guarantees those rows stay
// within the cold-start grace window right after deploy, so a healthy live
// generation is never gated before its sandbox has a chance to send its first
// post-deploy heartbeat. Idempotent: only touches NULL started_at.
func (s *Store) ensureRuntimeGenerationColumns(ctx context.Context) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"sandbox_last_seen_at", `ALTER TABLE runtime_generations ADD COLUMN sandbox_last_seen_at TEXT`},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, "runtime_generations", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE runtime_generations
SET started_at = last_seen_at
WHERE started_at IS NULL
  AND last_seen_at IS NOT NULL
  AND status NOT IN ('failed','destroyed')`); err != nil {
		return err
	}
	return nil
}

// backfillMessagesFTS indexes messages rows that predate the messages_fts
// vtable and its sync triggers. The triggers only cover writes made after the
// vtable exists, so on the first deploy (or after the index is ever cleared)
// history would otherwise be invisible to search.
//
// A full 'rebuild' is O(all rows) and must not run on every boot once the index
// is populated. We therefore only rebuild when the index is empty while
// messages is not. Emptiness is probed via the messages_fts_docsize shadow
// table (one row per indexed document): querying messages_fts itself with
// count()/rowid reads *through* to the external content table and so cannot
// tell an unindexed table from an indexed one. This is idempotent and safe
// across redeploys and rollbacks (a populated index is left untouched).
func (s *Store) backfillMessagesFTS(ctx context.Context) error {
	var indexedDocs int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages_fts_docsize`).Scan(&indexedDocs); err != nil {
		return err
	}
	if indexedDocs > 0 {
		// Index already populated; nothing to backfill.
		return nil
	}

	var messageRowID int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM messages LIMIT 1`).Scan(&messageRowID)
	if errors.Is(err, sql.ErrNoRows) {
		// No history to index yet; triggers will cover future writes.
		return nil
	}
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`)
	return err
}

// ensureSessionColumns adds session columns introduced after the original
// CREATE TABLE. ADD COLUMN with a constant DEFAULT is an O(1) metadata-only
// change in SQLite (no table rewrite, no lock), so existing rows read back the
// default without blocking online traffic. It is idempotent: pre-existing
// columns are skipped so redeploys and rollbacks are safe.
func (s *Store) ensureSessionColumns(ctx context.Context) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"title", `ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT ''`},
		{"title_custom", `ALTER TABLE sessions ADD COLUMN title_custom INTEGER NOT NULL DEFAULT 0`},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, "sessions", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureUserColumns upgrades databases created before R0. SQLite's constant
// DEFAULT makes this metadata-only and idempotent, so a mixed-version rollout
// can safely start the new binary against an existing control database.
func (s *Store) ensureUserColumns(ctx context.Context) error {
	exists, err := s.columnExists(ctx, "users", "role")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'`); err != nil {
			return err
		}
	}
	// ALTER TABLE cannot add a CHECK constraint in SQLite. Triggers provide the
	// same invariant for upgraded databases without a blocking table rebuild.
	if _, err := s.db.ExecContext(ctx, `
CREATE TRIGGER IF NOT EXISTS users_role_insert_guard
BEFORE INSERT ON users WHEN NEW.role NOT IN ('user','admin')
BEGIN SELECT RAISE(ABORT, 'invalid users.role'); END;
CREATE TRIGGER IF NOT EXISTS users_role_update_guard
BEFORE UPDATE OF role ON users WHEN NEW.role NOT IN ('user','admin')
BEGIN SELECT RAISE(ABORT, 'invalid users.role'); END;`); err != nil {
		return err
	}
	return nil
}

// ensureMessageColumns adds message columns introduced after the original
// CREATE TABLE. Same O(1) metadata-only guarantee and idempotency as
// ensureSessionColumns. stop_reason records pi's per-message terminator
// ("stop" for a turn's final reply, "toolUse" for an intermediate step, "" when
// unknown) so the UI can distinguish a completed answer from an interstitial.
func (s *Store) ensureMessageColumns(ctx context.Context) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"stop_reason", `ALTER TABLE messages ADD COLUMN stop_reason TEXT NOT NULL DEFAULT ''`},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, "messages", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureNotificationColumns adds the notification audience column to existing
// databases. Same O(1) metadata-only ALTER and idempotency as
// ensureSessionColumns.
//
// It is nullable with no default on purpose: existing rows read back NULL, which
// is exactly the broadcast semantics they already had, so the migration needs no
// backfill and a rollback keeps working.
func (s *Store) ensureNotificationColumns(ctx context.Context) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"user_id", `ALTER TABLE notifications ADD COLUMN user_id TEXT`},
	}
	for _, column := range columns {
		exists, err := s.columnExists(ctx, "notifications", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.ddl); err != nil {
			return err
		}
	}
	// Indexed here rather than in the schema DDL because the column above may not
	// exist until the ALTER just ran; see the note next to
	// notifications_created_at_idx.
	if _, err := s.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS notifications_user_idx
  ON notifications (user_id)`); err != nil {
		return err
	}
	return nil
}

// columnExists reports whether the given table has a column of the given name,
// via the pragma_table_info table-valued function, which accepts a bound
// parameter for the table name.
func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func quoteSQLiteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
