CREATE TABLE active_execution_adoptions (
 generation_id TEXT PRIMARY KEY REFERENCES execution_bindings(generation_id),
 turn_id INTEGER NOT NULL REFERENCES turns(id),
 attempt INTEGER NOT NULL,
 previous_owner TEXT NOT NULL,
 owner TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 event_sequence INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('adopting','accepted'))
);

CREATE TABLE admin_audit_log (
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

CREATE TABLE agent_schedules (
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

CREATE TABLE artifacts (
  session_id TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mod_time TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(session_id, path),
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  last_seen_at TEXT
);

CREATE TABLE bridge_ingress (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  generation_id TEXT NOT NULL,
  turn_id INTEGER NOT NULL,
  attempt INTEGER NOT NULL,
  record_key TEXT NOT NULL,
  output_sequence INTEGER,
  type TEXT NOT NULL,
  stream TEXT NOT NULL,
  payload TEXT NOT NULL,
  digest TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(generation_id, turn_id, attempt, record_key)
);

CREATE TABLE bridge_projected_messages (
  record_id INTEGER NOT NULL REFERENCES bridge_ingress(id),
  ordinal INTEGER NOT NULL,
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  PRIMARY KEY(record_id,ordinal),
  UNIQUE(message_id)
);

CREATE TABLE bridge_projections (
  generation_id TEXT NOT NULL,
  turn_id INTEGER NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
  attempt INTEGER NOT NULL,
  last_record_id INTEGER NOT NULL,
  state TEXT NOT NULL,
  PRIMARY KEY(generation_id,turn_id,attempt)
);

CREATE TABLE bridge_stream_receipts (
  backend_id TEXT NOT NULL,
  generation_id TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK(sequence>0),
  digest TEXT NOT NULL,
  PRIMARY KEY(backend_id,generation_id,sequence),
  FOREIGN KEY(backend_id,generation_id) REFERENCES bridge_streams(backend_id,generation_id)
);

CREATE TABLE bridge_streams (
  backend_id TEXT NOT NULL,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  generation_id TEXT NOT NULL,
  process_handle TEXT NOT NULL,
  epoch INTEGER NOT NULL CHECK(epoch>0),
  watermark INTEGER NOT NULL DEFAULT 0 CHECK(watermark>=0),
  PRIMARY KEY(backend_id,generation_id),
  UNIQUE(backend_id,process_handle)
);

CREATE TABLE deferred_turn_requests (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 generation_id TEXT NOT NULL REFERENCES execution_bindings(generation_id),
 request_id TEXT NOT NULL,
 envelope TEXT NOT NULL,
 command_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','delivered','obsolete')),
 UNIQUE(generation_id,request_id)
);

-- content_publications is the durable record of transporting one authored
-- revision into an immutable engine volume. The revision digest is content
-- identity, so the same authored bytes converge on one row and one published
-- volume, and different bytes can never claim an existing publication.
--
-- staging_volume_id is the prepared, writable source. It stays referenced
-- because the engine pins a snapshot's parent; it is disposable preparation,
-- never user data. published_volume_id is committed only after the published
-- bytes have been read back and matched against revision_digest.
CREATE TABLE content_publications (
 store_id TEXT NOT NULL,
 purpose TEXT NOT NULL CHECK(purpose IN ('assets','config')),
 revision_digest TEXT NOT NULL CHECK(revision_digest LIKE 'sha256:%'),
 capacity INTEGER NOT NULL CHECK(capacity>0),
 entries TEXT NOT NULL,
 staging_volume_id TEXT NOT NULL DEFAULT '',
 derived_volume_id TEXT NOT NULL DEFAULT '',
 published_volume_id TEXT NOT NULL DEFAULT '',
 stage TEXT NOT NULL CHECK(stage IN ('preparing','staged','derived','published','failed')),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(store_id,purpose,revision_digest),
 CHECK((stage='published') = (published_volume_id<>'')),
 CHECK(published_volume_id='' OR published_volume_id=derived_volume_id),
 CHECK((stage IN ('preparing','staged','failed')) OR derived_volume_id<>''),
 CHECK((stage IN ('preparing','failed')) OR staging_volume_id<>''),
 CHECK(staging_volume_id='' OR staging_volume_id<>derived_volume_id)
);

CREATE TABLE egress_allocations (
 generation_id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL REFERENCES sessions(id),
 environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
 store_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 environment_fence INTEGER NOT NULL,
 intent TEXT NOT NULL,
 bindings TEXT NOT NULL,
 created_at TEXT NOT NULL
);

CREATE TABLE egress_grants (
 grant_id TEXT PRIMARY KEY,
 binding_id TEXT NOT NULL REFERENCES egress_route_bindings(binding_id),
 generation_id TEXT NOT NULL REFERENCES runtime_generations(generation_id),
 turn_id INTEGER NOT NULL REFERENCES turns(id),
 policy_digest TEXT NOT NULL,
 binding_revision INTEGER NOT NULL,
 expires_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','revoked'))
);

CREATE TABLE egress_requests (
 backend_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 binding_id TEXT NOT NULL,
 grant_id TEXT NOT NULL,
 generation_id TEXT NOT NULL,
 turn_id INTEGER NOT NULL,
 epoch INTEGER NOT NULL,
 service_id TEXT NOT NULL,
 operation TEXT NOT NULL,
 started_at TEXT NOT NULL,
 outcome TEXT,
 bytes INTEGER,
 finished_at TEXT,
 PRIMARY KEY(backend_id,request_id)
);

CREATE TABLE egress_route_bindings (
 binding_id TEXT PRIMARY KEY,
 backend_id TEXT NOT NULL,
 generation_id TEXT NOT NULL,
 epoch INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 service_id TEXT NOT NULL,
 destination TEXT NOT NULL,
 gateway TEXT NOT NULL,
 policy_digest TEXT NOT NULL,
 network_digest TEXT NOT NULL,
 service TEXT NOT NULL,
 operations TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('dormant','active','revoked')),
 UNIQUE(backend_id,destination)
);

CREATE TABLE egress_tuple_pools (
 store_id TEXT PRIMARY KEY,
 synthetic_range TEXT NOT NULL,
 next_offset INTEGER NOT NULL CHECK(next_offset>0)
);

CREATE TABLE engine_backend_blocks (
 store_id TEXT PRIMARY KEY,
 reason TEXT NOT NULL
);

CREATE TABLE engine_event_cursors (
  store_id TEXT PRIMARY KEY,
  after_sequence INTEGER NOT NULL,
  resync_watermark INTEGER NOT NULL,
  resync INTEGER NOT NULL CHECK(resync IN (0,1))
);

CREATE TABLE engine_event_inbox (
  store_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  sandbox_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  epoch INTEGER NOT NULL,
  payload TEXT NOT NULL,
  digest TEXT NOT NULL,
  PRIMARY KEY(store_id,event_id),
  UNIQUE(store_id,sequence)
);

CREATE TABLE environment_operations (
  operation_id TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
  kind TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  fence INTEGER NOT NULL,
  mutation_revision TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','issued','uncertain','succeeded','failed')),
  request TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  result TEXT NOT NULL,
  error TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(environment_id,idempotency_key),
  UNIQUE(environment_id,fence)
);

CREATE TABLE environment_reconcile_requests (
  environment_id TEXT PRIMARY KEY REFERENCES sandbox_environments(environment_id),
  reason TEXT NOT NULL,
  event_sequence INTEGER NOT NULL
);

CREATE TABLE events (
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

CREATE TABLE execution_bindings (
  generation_id TEXT PRIMARY KEY REFERENCES runtime_generations(generation_id),
  session_id TEXT NOT NULL REFERENCES sessions(id),
  environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
  store_id TEXT NOT NULL,
  sandbox_id TEXT NOT NULL,
  process_handle TEXT NOT NULL DEFAULT '',
  epoch INTEGER NOT NULL CHECK(epoch>0),
  environment_fence INTEGER NOT NULL,
  owner TEXT NOT NULL,
  driver_id TEXT NOT NULL CHECK(driver_id IN ('pi','sh')),
  state TEXT NOT NULL CHECK(state IN ('starting','accepted','retired')),
  spec_digest TEXT NOT NULL,
  manifest_digest TEXT NOT NULL,
  protocol_version INTEGER NOT NULL,
  turn_input_schema TEXT NOT NULL
);

CREATE TABLE execution_recoveries (
 generation_id TEXT PRIMARY KEY REFERENCES execution_bindings(generation_id),
 environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
 reason TEXT NOT NULL,
 stage TEXT NOT NULL CHECK(stage IN ('pause','resume','replay','final_pause','complete')),
 operation_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

CREATE TABLE execution_steering (
 command_id TEXT PRIMARY KEY REFERENCES execution_turn_commands(command_id),
 queued_turn_id INTEGER NOT NULL UNIQUE REFERENCES turns(id),
 outcome TEXT NOT NULL DEFAULT 'pending' CHECK(outcome IN ('pending','delivered','rejected','unknown')),
 message_id INTEGER REFERENCES messages(id),
 event_id INTEGER REFERENCES events(event_id)
);

CREATE TABLE execution_turn_commands (
 command_id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL REFERENCES sessions(id),
 generation_id TEXT NOT NULL REFERENCES execution_bindings(generation_id),
 turn_id INTEGER NOT NULL REFERENCES turns(id),
 owner TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('abort','steer')),
 message TEXT NOT NULL,
 acknowledged INTEGER NOT NULL DEFAULT 0 CHECK(acknowledged IN (0,1)),
 created_at TEXT NOT NULL
);

CREATE TABLE execution_turn_requests (
 generation_id TEXT NOT NULL REFERENCES execution_bindings(generation_id),
 request_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('claim','resume')),
 requested_turn_id INTEGER NOT NULL,
 granted INTEGER NOT NULL CHECK(granted IN (0,1)),
 grant_payload TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(generation_id,request_id)
);

CREATE TABLE feature_flags (
  key TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  description TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE idle_execution_adoptions (
 generation_id TEXT PRIMARY KEY REFERENCES execution_bindings(generation_id),
 owner TEXT NOT NULL,
 event_sequence INTEGER NOT NULL
);

CREATE TABLE message_feedback (
  message_id INTEGER NOT NULL,
  user_id TEXT NOT NULL,
  rating TEXT NOT NULL CHECK(rating IN ('up','down')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(message_id, user_id),
  FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE
);

CREATE TABLE message_traces (
  message_id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL,
  nodes TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  -- turn_id links user input to its durable turn. Nullable for historical and
  -- system messages; new enqueued user messages always set it atomically.
  turn_id INTEGER,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL,
  stop_reason TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE messages_fts USING fts5(
  content,
  content='messages',
  content_rowid='id',
  tokenize='trigram'
);

CREATE TABLE native_capture_activations (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id),
 environment_id TEXT NOT NULL UNIQUE REFERENCES sandbox_environments(environment_id),
 through_version INTEGER NOT NULL,
 attempt INTEGER NOT NULL DEFAULT 1,
 stage TEXT NOT NULL CHECK(stage IN ('requested','resuming','reading','pausing','complete')),
 operation_id TEXT NOT NULL DEFAULT '',
 last_pause_operation_id TEXT NOT NULL DEFAULT '',
 deadline TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);

CREATE TABLE native_capture_receipts (
 session_id TEXT NOT NULL,
 state_version INTEGER NOT NULL,
 native_digest TEXT NOT NULL,
 archive_path TEXT NOT NULL,
 completed_at TEXT NOT NULL,
 PRIMARY KEY(session_id,state_version),
 FOREIGN KEY(session_id,state_version) REFERENCES native_capture_requests(session_id,state_version)
);

CREATE TABLE native_capture_requests (
 session_id TEXT NOT NULL REFERENCES sessions(id),
 state_version INTEGER NOT NULL CHECK(state_version>1),
 state_digest TEXT NOT NULL,
 generation_id TEXT NOT NULL REFERENCES execution_bindings(generation_id),
 turn_id INTEGER NOT NULL REFERENCES turns(id),
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 native_id TEXT NOT NULL,
 selected_path TEXT NOT NULL,
 snapshot_path TEXT NOT NULL,
 native_digest TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>0),
 created_at TEXT NOT NULL,
 PRIMARY KEY(session_id,state_version),
 UNIQUE(session_id,state_digest),
 UNIQUE(generation_id,turn_id)
);

CREATE TABLE notification_reads (
  notification_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  read_at TEXT NOT NULL,
  PRIMARY KEY (notification_id, user_id),
  FOREIGN KEY(notification_id) REFERENCES notifications(id) ON DELETE CASCADE
);

CREATE TABLE notifications (
  id TEXT PRIMARY KEY,
  user_id TEXT,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE orchestrator_owner (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  uuid TEXT NOT NULL,
  boot_id TEXT NOT NULL,
  host_run_dir TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  heartbeat_at TEXT NOT NULL
);

CREATE TABLE runtime_generations (
  generation_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('allocating','starting','probing','active','idle','pausing','paused','restoring','failed','destroyed')),
  lease_owner TEXT,
  lease_expires_at TEXT,
  started_at TEXT,
  last_seen_at TEXT,
  sandbox_last_seen_at TEXT,
  ended_at TEXT,
  failure_reason TEXT,
  error_class TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE sandbox_backends (
  name TEXT PRIMARY KEY,
  store_id TEXT NOT NULL,
  version TEXT NOT NULL,
  UNIQUE(name,store_id)
);

CREATE TABLE sandbox_environments (
  environment_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id),
  backend_name TEXT NOT NULL,
  store_id TEXT NOT NULL,
  sandbox_id TEXT,
  desired TEXT NOT NULL,
  fence INTEGER NOT NULL DEFAULT 0 CHECK(fence>=0 AND typeof(fence)='integer'),
  mutation_revision TEXT NOT NULL DEFAULT '',
  epoch INTEGER NOT NULL DEFAULT 0 CHECK(epoch>=0),
  observed_state TEXT NOT NULL DEFAULT 'unknown' CHECK(observed_state IN ('unknown','running','paused')),
  FOREIGN KEY(backend_name,store_id) REFERENCES sandbox_backends(name,store_id),
  UNIQUE(store_id,sandbox_id)
);

CREATE TABLE schedule_runs (
  id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT,
  turn_id INTEGER,
  scheduled_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','timed_out','canceled','skipped_busy')),
  attempt INTEGER NOT NULL DEFAULT 0,
  error_class TEXT,
  error TEXT,
  output_summary TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY(schedule_id) REFERENCES agent_schedules(id) ON DELETE CASCADE,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE SET NULL,
  FOREIGN KEY(turn_id) REFERENCES turns(id) ON DELETE SET NULL
);

CREATE TABLE session_driver_states (
  session_id TEXT NOT NULL,
  driver_id TEXT NOT NULL CHECK(driver_id <> '' AND driver_id IN ('pi','sh')),
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

CREATE TABLE session_retirements (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id),
 state TEXT NOT NULL CHECK(state IN ('requested','complete')),
 event_id INTEGER REFERENCES events(event_id),
 created_at TEXT NOT NULL
);

CREATE TABLE session_shares (
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

CREATE TABLE session_storage_bindings (
 session_id TEXT NOT NULL REFERENCES sessions(id),
 environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
 purpose TEXT NOT NULL CHECK(purpose IN ('workspace','bridge')),
 store_id TEXT NOT NULL,
 volume_id TEXT NOT NULL,
 capacity INTEGER NOT NULL CHECK(capacity>0),
 PRIMARY KEY(session_id,purpose),
 UNIQUE(store_id,volume_id)
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('created','running_active','running_idle','pausing','paused','failed','destroyed')),
  driver_id TEXT NOT NULL CHECK(driver_id IN ('pi','sh')),
  mode TEXT NOT NULL CHECK(mode IN ('agent','shell')),
  workspace TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ended_at TEXT,
  last_activity_at TEXT,
  active_generation_id TEXT REFERENCES runtime_generations(generation_id),
  failure_reason TEXT,
  error_class TEXT,
  title TEXT NOT NULL DEFAULT '',
  title_custom INTEGER NOT NULL DEFAULT 0 CHECK(title_custom IN (0,1)),
  turn_revision INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE share_link_files (
  link_id      TEXT NOT NULL,
  path         TEXT NOT NULL,
  blob_sha256  TEXT NOT NULL,
  size         INTEGER NOT NULL,
  PRIMARY KEY(link_id, path),
  FOREIGN KEY(link_id) REFERENCES share_links(id) ON DELETE CASCADE
);

CREATE TABLE share_links (
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

CREATE TABLE turns (
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
  -- Durable activity projection for the turn. The UI's "what is the agent doing
  -- right now" strip reads these instead of replaying the event stream: the last
  -- event for a running turn is usually an agent.delta, so reconstructing the
  -- phase from it reported "responding" while a tool was actually executing.
  -- Written on coarse transitions only (turn start, tool start/end, compaction,
  -- retry) — never per delta.
  phase TEXT NOT NULL DEFAULT '' CHECK(phase IN ('','thinking','tool','streaming','compacting','retrying')),
  phase_at TEXT,
  phase_tool TEXT,
  phase_detail TEXT,
  -- Whether dispatching this turn creates a transcript entry in messages. User
  -- turns set it; automatic continuations (ResumeFailedTurn) do not, because the
  -- user never typed them. The message row is written at dispatch, not at enqueue
  -- (see materializeTurnMessageTx for why).
  materializes_message INTEGER NOT NULL DEFAULT 0 CHECK(materializes_message IN (0,1)),
  -- Whether the worker may claim this turn on its own. Cleared for the turns
  -- still queued when the user presses Stop: Stop means "stop and do not
  -- continue", so the rest of the queue is parked rather than discarded, and the
  -- user releases each prompt themselves.
  --
  -- A parked turn is dormant, not pending. It does not keep the generation
  -- active, block a checkpoint, or hold a sandbox: releasing it costs a cold
  -- start, exactly as typing it fresh would. Every "is there pending work"
  -- predicate therefore also requires auto_dispatch = 1 (see
  -- pendingTurnPredicate), which is sound because only a queued turn is ever
  -- parked — see parkQueuedTurnsTx.
  auto_dispatch INTEGER NOT NULL DEFAULT 1 CHECK(auto_dispatch IN (0,1)),
  -- Set when this turn's content was delivered by steering it into another turn
  -- that was already running, instead of being dispatched as a turn of its own.
  -- pi's steer injects into the live turn and produces no turn of its own, so the
  -- row is retired as completed and this records where its content went.
  steered_into_turn_id INTEGER,
  FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE,
  FOREIGN KEY(generation_id) REFERENCES runtime_generations(generation_id),
  FOREIGN KEY(completed_by_generation) REFERENCES runtime_generations(generation_id),
  FOREIGN KEY(steered_into_turn_id) REFERENCES turns(id)
);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('user','admin')),
  created_at TEXT NOT NULL
);

CREATE TABLE workspace_guest_mutations (
 operation_id TEXT PRIMARY KEY REFERENCES workspace_mutations(operation_id),
 reference TEXT NOT NULL,
 content_digest TEXT NOT NULL,
 content_size INTEGER NOT NULL CHECK(content_size>=0),
 issued INTEGER NOT NULL DEFAULT 0 CHECK(issued IN (0,1))
);

CREATE TABLE workspace_inventory_scans (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id),
 scan_id TEXT NOT NULL,
 owner TEXT NOT NULL,
 environment_id TEXT NOT NULL,
 fence INTEGER NOT NULL,
 reference TEXT NOT NULL,
 event_sequence INTEGER NOT NULL,
 mutation_sequence INTEGER NOT NULL,
 started_at TEXT NOT NULL
);

CREATE TABLE workspace_mutations (
  operation_id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  owner TEXT NOT NULL,
  path TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('active','uncertain','succeeded','failed')),
  result TEXT NOT NULL,
  error TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE workspace_read_leases (
 lease_id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL REFERENCES sessions(id),
 environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
 owner TEXT NOT NULL,
 epoch INTEGER NOT NULL,
 mutation_revision TEXT NOT NULL,
 volume_id TEXT NOT NULL,
 created_at TEXT NOT NULL
);

CREATE TABLE workspace_write_maintenance (
 mutation_id TEXT PRIMARY KEY REFERENCES workspace_mutations(operation_id),
 environment_id TEXT NOT NULL REFERENCES sandbox_environments(environment_id),
 stage TEXT NOT NULL CHECK(stage IN ('resuming','writing','pausing','complete')),
 operation_id TEXT NOT NULL DEFAULT '',
 outcome TEXT NOT NULL DEFAULT '' CHECK(outcome IN ('','succeeded','failed'))
);

CREATE INDEX admin_audit_log_actor_idx ON admin_audit_log(actor_user_id, created_at DESC);

CREATE INDEX admin_audit_log_created_idx ON admin_audit_log(created_at DESC, id DESC);

CREATE INDEX agent_schedules_due_idx ON agent_schedules(enabled, next_run_at);

CREATE INDEX agent_schedules_user_idx ON agent_schedules(user_id, created_at DESC);

CREATE INDEX auth_sessions_expires_idx ON auth_sessions(expires_at);

-- One volume is one publication. Without this, a replayed engine key observed
-- under two rows would let a second revision claim the first's published bytes.
CREATE UNIQUE INDEX content_publications_published_idx
  ON content_publications(store_id, published_volume_id)
  WHERE published_volume_id <> '';

CREATE UNIQUE INDEX content_publications_staging_idx
  ON content_publications(store_id, staging_volume_id)
  WHERE staging_volume_id <> '';

CREATE INDEX auth_sessions_user_idx ON auth_sessions(user_id);

CREATE INDEX bridge_ingress_stream_idx ON bridge_ingress(generation_id,turn_id,attempt,id);

CREATE UNIQUE INDEX deferred_turn_reply_uq
 ON deferred_turn_requests(generation_id,command_id) WHERE command_id!='';

CREATE INDEX engine_event_sandbox_sequence_idx ON engine_event_inbox(store_id,sandbox_id,sequence);

CREATE UNIQUE INDEX environment_operations_unresolved_uq
  ON environment_operations(environment_id) WHERE state IN ('pending','issued','uncertain');

CREATE INDEX events_created_at_idx
  ON events (created_at);

CREATE UNIQUE INDEX events_dedupe_key_uq
  ON events (session_id, dedupe_key)
  WHERE dedupe_key IS NOT NULL;

CREATE UNIQUE INDEX events_output_dedupe_uq
  ON events (turn_id, generation_id, output_sequence)
  WHERE output_sequence IS NOT NULL;

CREATE UNIQUE INDEX events_proxy_finished_request_uq
  ON events (proxy_request_id)
  WHERE proxy_request_id IS NOT NULL
    AND type IN ('proxy.request.completed', 'proxy.request.failed');

CREATE INDEX events_proxy_request_id_idx
  ON events (proxy_request_id);

CREATE UNIQUE INDEX events_proxy_started_request_uq
  ON events (proxy_request_id)
  WHERE proxy_request_id IS NOT NULL
    AND type = 'proxy.request.started';

CREATE INDEX events_session_event_id_idx
  ON events (session_id, event_id);

CREATE UNIQUE INDEX execution_binding_process_uq ON execution_bindings(store_id,process_handle) WHERE process_handle!='';

CREATE UNIQUE INDEX execution_recoveries_active_uq
 ON execution_recoveries(environment_id) WHERE stage!='complete';

CREATE INDEX message_feedback_message_idx
  ON message_feedback (message_id);

CREATE INDEX message_traces_session_idx
  ON message_traces (session_id);

CREATE INDEX messages_session_idx ON messages(session_id, id);

CREATE INDEX messages_turn_idx ON messages(turn_id);

CREATE INDEX notification_reads_user_idx
  ON notification_reads (user_id);

CREATE INDEX notifications_created_at_idx
  ON notifications (created_at);

CREATE INDEX notifications_user_idx
  ON notifications (user_id);

CREATE UNIQUE INDEX runtime_generations_one_nonterminal_per_session
  ON runtime_generations (session_id)
  WHERE status NOT IN ('failed', 'destroyed');

CREATE INDEX runtime_generations_session_status_idx
  ON runtime_generations (session_id, status);

CREATE INDEX schedule_runs_schedule_idx ON schedule_runs(schedule_id, scheduled_at DESC);

CREATE UNIQUE INDEX schedule_runs_schedule_slot_uq ON schedule_runs(schedule_id, scheduled_at, attempt);

CREATE INDEX schedule_runs_status_idx ON schedule_runs(status, started_at);

CREATE INDEX schedule_runs_user_idx ON schedule_runs(user_id, scheduled_at DESC);

CREATE INDEX session_shares_grantee_idx ON session_shares (grantee_user_id);

CREATE UNIQUE INDEX session_shares_live_idx
  ON session_shares (session_id, grantee_user_id) WHERE revoked_at IS NULL;

CREATE INDEX sessions_active_generation_idx ON sessions(active_generation_id);

CREATE INDEX sessions_status_idx ON sessions(status);

CREATE INDEX share_links_session_idx ON share_links (session_id);

CREATE UNIQUE INDEX turns_claim_request_id_uq
  ON turns (session_id, claim_request_id)
  WHERE claim_request_id IS NOT NULL;

CREATE UNIQUE INDEX turns_session_sequence_uq
  ON turns (session_id, sequence);

CREATE INDEX turns_session_status_sequence_idx
  ON turns (session_id, status, sequence);

CREATE UNIQUE INDEX workspace_mutation_active_uq
 ON workspace_mutations(session_id) WHERE state IN ('active','uncertain');

CREATE INDEX workspace_read_leases_environment_idx ON workspace_read_leases(environment_id);

CREATE TRIGGER execution_binding_revision AFTER UPDATE OF state,process_handle,epoch ON execution_bindings
WHEN NEW.state!=OLD.state OR NEW.process_handle!=OLD.process_handle OR NEW.epoch!=OLD.epoch
BEGIN UPDATE sessions SET turn_revision=turn_revision+1 WHERE id=NEW.session_id; END;

CREATE TRIGGER execution_invalidation_revision AFTER INSERT ON environment_reconcile_requests
BEGIN UPDATE sessions SET turn_revision=turn_revision+1 WHERE id=(SELECT session_id FROM sandbox_environments WHERE environment_id=NEW.environment_id); END;

CREATE TRIGGER execution_revalidated_revision AFTER DELETE ON environment_reconcile_requests
BEGIN UPDATE sessions SET turn_revision=turn_revision+1 WHERE id=(SELECT session_id FROM sandbox_environments WHERE environment_id=OLD.environment_id); END;

CREATE TRIGGER retire_execution_binding AFTER UPDATE OF status ON runtime_generations
WHEN NEW.status IN ('failed','destroyed')
BEGIN
 UPDATE execution_bindings SET state='retired' WHERE generation_id=NEW.generation_id;
 UPDATE egress_route_bindings SET state='revoked' WHERE generation_id=NEW.generation_id;
 UPDATE egress_grants SET state='revoked' WHERE generation_id=NEW.generation_id;
END;

CREATE TRIGGER revoke_completed_turn_grants AFTER UPDATE OF status ON turns
WHEN NEW.status IN ('completed','failed','canceled')
BEGIN UPDATE egress_grants SET state='revoked' WHERE turn_id=NEW.id; END;

CREATE TRIGGER runtime_generations_runtime_revision_delete
AFTER DELETE ON runtime_generations
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1
  WHERE active_generation_id = OLD.generation_id;
END;

CREATE TRIGGER runtime_generations_runtime_revision_insert
AFTER INSERT ON runtime_generations
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1
  WHERE active_generation_id = NEW.generation_id;
END;

CREATE TRIGGER runtime_generations_runtime_revision_update
AFTER UPDATE ON runtime_generations
WHEN OLD.status IS NOT NEW.status
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1
  WHERE active_generation_id = NEW.generation_id;
END;

CREATE TRIGGER sessions_runtime_revision
AFTER UPDATE OF status, active_generation_id ON sessions
WHEN OLD.status IS NOT NEW.status OR OLD.active_generation_id IS NOT NEW.active_generation_id
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1 WHERE id = NEW.id;
END;

CREATE TRIGGER steering_pending_turn_guard BEFORE UPDATE OF status,auto_dispatch ON turns
WHEN EXISTS(SELECT 1 FROM execution_steering s WHERE s.queued_turn_id=NEW.id AND s.outcome='pending')
 AND (NEW.status!='queued' OR NEW.auto_dispatch!=0)
BEGIN SELECT RAISE(ABORT,'steering outcome unresolved'); END;

CREATE TRIGGER steering_runtime_revision_insert AFTER INSERT ON execution_steering
BEGIN UPDATE sessions SET turn_revision=turn_revision+1 WHERE id=(SELECT session_id FROM turns WHERE id=NEW.queued_turn_id); END;

CREATE TRIGGER steering_runtime_revision_update AFTER UPDATE OF outcome ON execution_steering
WHEN NEW.outcome!=OLD.outcome
BEGIN UPDATE sessions SET turn_revision=turn_revision+1 WHERE id=(SELECT session_id FROM turns WHERE id=NEW.queued_turn_id); END;

CREATE TRIGGER turns_revision_delete
AFTER DELETE ON turns
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1 WHERE id = OLD.session_id;
END;

CREATE TRIGGER turns_revision_insert
AFTER INSERT ON turns
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1 WHERE id = NEW.session_id;
END;

CREATE TRIGGER turns_revision_update
AFTER UPDATE ON turns
WHEN OLD.status <> NEW.status
  OR COALESCE(OLD.phase,'') <> COALESCE(NEW.phase,'')
  OR COALESCE(OLD.phase_tool,'') <> COALESCE(NEW.phase_tool,'')
  OR COALESCE(OLD.phase_detail,'') <> COALESCE(NEW.phase_detail,'')
  OR OLD.auto_dispatch <> NEW.auto_dispatch
BEGIN
  UPDATE sessions SET turn_revision = turn_revision + 1 WHERE id = NEW.session_id;
END;

CREATE TRIGGER users_role_insert_guard
BEFORE INSERT ON users WHEN NEW.role NOT IN ('user','admin')
BEGIN SELECT RAISE(ABORT, 'invalid users.role'); END;

CREATE TRIGGER users_role_update_guard
BEFORE UPDATE OF role ON users WHEN NEW.role NOT IN ('user','admin')
BEGIN SELECT RAISE(ABORT, 'invalid users.role'); END;

CREATE TRIGGER workspace_fence_generation_insert BEFORE INSERT ON runtime_generations
WHEN EXISTS(SELECT 1 FROM workspace_mutations WHERE session_id=NEW.session_id AND state IN ('active','uncertain'))
BEGIN SELECT RAISE(ABORT,'workspace mutation in progress'); END;

CREATE TRIGGER workspace_fence_generation_transition BEFORE UPDATE OF status ON runtime_generations
WHEN NEW.status!=OLD.status AND EXISTS(
 SELECT 1 FROM workspace_mutations WHERE session_id=NEW.session_id AND state IN ('active','uncertain'))
BEGIN SELECT RAISE(ABORT,'workspace mutation in progress'); END;

CREATE TRIGGER workspace_fence_session_transition BEFORE UPDATE OF status,active_generation_id ON sessions
WHEN (NEW.status!=OLD.status OR NEW.active_generation_id IS NOT OLD.active_generation_id) AND EXISTS(
 SELECT 1 FROM workspace_mutations WHERE session_id=NEW.id AND state IN ('active','uncertain'))
BEGIN SELECT RAISE(ABORT,'workspace mutation in progress'); END;

CREATE TRIGGER workspace_fence_turn_dispatch BEFORE UPDATE OF status,auto_dispatch ON turns
WHEN NEW.status IN ('queued','leased','running') AND NEW.auto_dispatch=1 AND EXISTS(
 SELECT 1 FROM workspace_mutations WHERE session_id=NEW.session_id AND state IN ('active','uncertain'))
BEGIN SELECT RAISE(ABORT,'workspace mutation in progress'); END;

CREATE TRIGGER workspace_fence_turn_insert BEFORE INSERT ON turns
WHEN NEW.status IN ('queued','leased','running') AND EXISTS(
 SELECT 1 FROM workspace_mutations WHERE session_id=NEW.session_id AND state IN ('active','uncertain'))
BEGIN SELECT RAISE(ABORT,'workspace mutation in progress'); END;

INSERT INTO feature_flags VALUES('admin_console',0,'Enable administrator console APIs','2026-09-20T00:00:00Z','');
INSERT INTO feature_flags VALUES('cron_agent',1,'Enable scheduled agent work','2026-09-20T00:00:00Z','');

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, old.content);
  INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
