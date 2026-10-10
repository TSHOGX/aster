-- Current product schema. New databases only; no historical schema or data upgrades.

CREATE TABLE agent_commands (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 incarnation_id TEXT NOT NULL,
 turn_id TEXT,
 kind TEXT NOT NULL CHECK(kind IN ('prompt','steer','abort','get_state')),
 payload BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','sending','dispatching','applied','rejected','completed','unknown')),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id,incarnation_id) REFERENCES agent_incarnations(project_id,session_id,id),
 FOREIGN KEY(project_id,session_id,turn_id) REFERENCES turns(project_id,session_id,id)
);

CREATE TABLE agent_incarnations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 handle TEXT NOT NULL,
 owner INTEGER NOT NULL CHECK(owner>0),
 state TEXT NOT NULL CHECK(state IN ('starting','running','stopping','exited','unknown')),
 last_work_at TEXT NOT NULL DEFAULT '',
 cleanup_state TEXT NOT NULL DEFAULT 'unknown' CHECK(cleanup_state IN ('unknown','settled')),
 recovery_state TEXT NOT NULL DEFAULT 'none' CHECK(recovery_state IN ('none','pending','replaying','complete','failed')),
 recovery_attempted TEXT NOT NULL DEFAULT '',
 ingested_sequence INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 UNIQUE(project_id,session_id,id),
 UNIQUE(store_id,sandbox_id,epoch,handle)
);

CREATE TABLE agent_records (
 incarnation_id TEXT NOT NULL REFERENCES agent_incarnations(id),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 digest TEXT NOT NULL,
 command_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 payload BLOB NOT NULL,
 PRIMARY KEY(incarnation_id,sequence)
);

CREATE TABLE application_release (
 id INTEGER PRIMARY KEY CHECK(id=1),
 generation INTEGER NOT NULL DEFAULT 0 CHECK(generation>=0),
 owner TEXT NOT NULL DEFAULT '',
 phase TEXT NOT NULL DEFAULT 'open' CHECK(phase IN ('open','draining','ready','activating')),
 deadline TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT ''
, current_instance TEXT NOT NULL DEFAULT '', activation_instance TEXT NOT NULL DEFAULT '', target_digest TEXT NOT NULL DEFAULT '', finished_digest TEXT NOT NULL DEFAULT '', mode TEXT NOT NULL DEFAULT 'quiescent' CHECK(mode IN ('quiescent','owner')), compatibility_digest TEXT NOT NULL DEFAULT '', schema_digest TEXT NOT NULL DEFAULT '', gateway_snapshot TEXT NOT NULL DEFAULT '');

CREATE TABLE accounts (
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL UNIQUE,
 display_name TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled')),
 role TEXT NOT NULL CHECK(role IN ('user','admin')),
 password_hash TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 session_revision INTEGER NOT NULL DEFAULT 1 CHECK(session_revision>0),
 authorization_revision INTEGER NOT NULL DEFAULT 1 CHECK(authorization_revision>0),
 must_change_password INTEGER NOT NULL DEFAULT 0 CHECK(must_change_password IN (0,1)),
 temporary_expires_at TEXT NOT NULL DEFAULT '',
 temporary_consumed INTEGER NOT NULL DEFAULT 0 CHECK(temporary_consumed IN (0,1)),
 last_login_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

CREATE TABLE account_preferences (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 theme_mode TEXT NOT NULL CHECK(theme_mode IN ('system','light','dark')),
 accent TEXT NOT NULL CHECK(accent IN ('neutral','blue','green','violet'))
);

CREATE TABLE auth_sessions (
 token_digest TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id),
 session_revision INTEGER NOT NULL,
 restricted INTEGER NOT NULL CHECK(restricted IN (0,1)),
 expires_at TEXT NOT NULL
);
CREATE INDEX auth_sessions_account ON auth_sessions(account_id);

CREATE TABLE audit_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 actor_id TEXT NOT NULL,
 action TEXT NOT NULL,
 target_kind TEXT NOT NULL,
 target_id TEXT NOT NULL,
 request_id TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL,
 details TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(details)),
 created_at TEXT NOT NULL
);
CREATE INDEX audit_events_actor ON audit_events(actor_id,id);
CREATE INDEX audit_events_target ON audit_events(target_kind,target_id,id);
CREATE INDEX audit_events_action ON audit_events(action,id);
CREATE INDEX audit_events_time ON audit_events(created_at,id);

CREATE TABLE admin_receipts (
 actor_id TEXT NOT NULL REFERENCES accounts(id),
 request_id TEXT NOT NULL,
 action TEXT NOT NULL,
 target_id TEXT NOT NULL,
 body_digest TEXT NOT NULL CHECK(length(body_digest)=64),
 result TEXT NOT NULL CHECK(json_valid(result)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(actor_id,request_id)
);
CREATE TABLE admin_operations (
 id TEXT PRIMARY KEY,
 actor_id TEXT NOT NULL REFERENCES accounts(id),
 action TEXT NOT NULL,
 target_kind TEXT NOT NULL,
 target_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('accepted','running','succeeded','failed','unknown')),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE admin_operation_targets (
 operation_id TEXT NOT NULL REFERENCES admin_operations(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 state TEXT NOT NULL DEFAULT 'accepted' CHECK(state IN ('accepted','running','succeeded','failed','unknown')),
 error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(operation_id,project_id)
);

CREATE TABLE account_groups (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL UNIQUE,
 description TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE permission_policies (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL UNIQUE,
 description TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 statements TEXT NOT NULL CHECK(json_valid(statements)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE group_policy_bindings (
 group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
 policy_id TEXT NOT NULL REFERENCES permission_policies(id) ON DELETE CASCADE,
 PRIMARY KEY(group_id,policy_id)
);
CREATE TABLE account_memberships (
 account_id TEXT NOT NULL REFERENCES accounts(id),
 group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
 PRIMARY KEY(account_id,group_id)
);
CREATE TABLE group_resource_policy (
 group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
 dimension TEXT NOT NULL,
 default_value INTEGER NOT NULL CHECK(default_value>=0),
 ceiling INTEGER NOT NULL CHECK(ceiling>=default_value),
 PRIMARY KEY(group_id,dimension)
);
CREATE TABLE account_resource_settings (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id),
 revision INTEGER NOT NULL CHECK(revision>0),
 updated_at TEXT NOT NULL
);
CREATE TABLE account_resource_overrides (
 account_id TEXT NOT NULL REFERENCES account_resource_settings(account_id) ON DELETE CASCADE,
 dimension TEXT NOT NULL,
 requested INTEGER NOT NULL CHECK(requested>=0),
 PRIMARY KEY(account_id,dimension)
);
CREATE TABLE platform_resource_limits (
 dimension TEXT PRIMARY KEY,
 maximum INTEGER NOT NULL CHECK(maximum>=0),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0)
);
CREATE TABLE registered_sandboxes (
 id TEXT NOT NULL,
 version TEXT NOT NULL,
 digest TEXT NOT NULL,
 store_id TEXT NOT NULL,
 template_id TEXT NOT NULL,
 manifest TEXT NOT NULL CHECK(json_valid(manifest)),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(id,version)
);
CREATE TABLE builtin_volumes (
 id TEXT NOT NULL,
 version TEXT NOT NULL,
 digest TEXT NOT NULL,
 store_id TEXT NOT NULL,
 publication_purpose TEXT NOT NULL,
 minimum_bytes INTEGER NOT NULL CHECK(minimum_bytes>0),
 roles TEXT NOT NULL CHECK(json_valid(roles)),
 clean_runtime INTEGER NOT NULL CHECK(clean_runtime IN (0,1)),
 entries TEXT NOT NULL CHECK(json_valid(entries)),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(id,version)
);
CREATE TABLE default_environment (
 id INTEGER PRIMARY KEY CHECK(id=1),
 revision INTEGER NOT NULL CHECK(revision>0),
 document TEXT NOT NULL CHECK(json_valid(document)),
 updated_at TEXT NOT NULL
);

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

CREATE TABLE conversation_changes (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 ordinal INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 operation TEXT NOT NULL CHECK(operation IN ('upsert','delete')),
 data TEXT NOT NULL, retained_bytes INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(session_id,revision,ordinal)
);

CREATE TABLE conversation_checkpoints (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 source_cursor INTEGER NOT NULL DEFAULT 0,
 content_revision INTEGER NOT NULL DEFAULT 0,
 index_revision INTEGER NOT NULL DEFAULT 0,
 next_position INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'building' CHECK(state IN ('building','ready','catching_up','failed')),
 reducer_state TEXT NOT NULL DEFAULT '{}',
 error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);

CREATE TABLE conversation_detail_gc (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 item_id TEXT NOT NULL,
 item_revision INTEGER NOT NULL,
 PRIMARY KEY(session_id,item_id,item_revision)
);

CREATE TABLE conversation_details (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 item_id TEXT NOT NULL,
 item_revision INTEGER NOT NULL,
 content TEXT NOT NULL,
 PRIMARY KEY(session_id,item_id,item_revision)
);

CREATE TABLE conversation_dirty_turns (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 turn_id TEXT NOT NULL,
 PRIMARY KEY(session_id,turn_id)
);

CREATE TABLE conversation_grants (
 session_id TEXT NOT NULL REFERENCES sessions(id),
 reader_id TEXT NOT NULL REFERENCES accounts(id) CHECK(reader_id<>''),
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)),
 PRIMARY KEY(session_id,reader_id)
);

CREATE TABLE conversation_items (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 position INTEGER NOT NULL,
 block_index INTEGER NOT NULL,
 item_revision INTEGER NOT NULL,
 source_key TEXT NOT NULL,
 data TEXT NOT NULL, message_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(session_id,id)
);

CREATE TABLE conversation_message_groups (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 incarnation_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 start_sequence INTEGER NOT NULL,
 position INTEGER NOT NULL,
 ended INTEGER NOT NULL DEFAULT 0 CHECK(ended IN (0,1)),
 PRIMARY KEY(session_id,incarnation_id,turn_id)
);

CREATE TABLE conversation_pending_sessions (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE conversation_retention (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
 change_count INTEGER NOT NULL DEFAULT 0,
 change_bytes INTEGER NOT NULL DEFAULT 0,
	pruning INTEGER NOT NULL DEFAULT 0 CHECK(pruning IN (0,1)),
 pruned_revision INTEGER NOT NULL DEFAULT 0,
 pruned_ordinal INTEGER NOT NULL DEFAULT -1
);

CREATE TABLE conversation_sources (
 source_id INTEGER PRIMARY KEY AUTOINCREMENT,
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 incarnation_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 UNIQUE(incarnation_id,sequence)
);

CREATE TABLE conversation_turns (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 turn_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 data TEXT NOT NULL,
 PRIMARY KEY(session_id,turn_id)
);

CREATE TABLE events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL REFERENCES projects(id),
 session_id TEXT,
 terminal_id TEXT,
 revision INTEGER NOT NULL CHECK(revision>0),
 kind TEXT NOT NULL,
 created_at TEXT NOT NULL,
 CHECK(session_id IS NULL OR terminal_id IS NULL),
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 FOREIGN KEY(project_id,terminal_id) REFERENCES terminals(project_id,id)
);

CREATE TABLE file_operations (
 action TEXT NOT NULL CHECK(action IN ('files.upload','files.edit','files.manage')),
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 request TEXT NOT NULL,
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>=0),
 handle TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('running','succeeded','failed','unknown')),
 settled INTEGER NOT NULL DEFAULT 0 CHECK(settled IN (0,1)),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 volume_id TEXT NOT NULL DEFAULT '',
 staging_path TEXT NOT NULL DEFAULT '',
 CHECK((volume_id='' AND epoch>0) OR (volume_id<>'' AND sandbox_id='' AND epoch=0 AND handle=''))
);

CREATE VIRTUAL TABLE message_search USING fts5(content,content='messages',content_rowid='rowid');

CREATE TABLE messages (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('user','assistant')),
 content TEXT NOT NULL,
 native_message BLOB,
 sequence INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id,turn_id) REFERENCES turns(project_id,session_id,id),
 UNIQUE(session_id,sequence)
);

CREATE TABLE native_captures (
 turn_id TEXT NOT NULL REFERENCES turns(id),
 incarnation_id TEXT NOT NULL REFERENCES agent_incarnations(id),
 path TEXT PRIMARY KEY,
 digest TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>=0),
 host_path TEXT NOT NULL DEFAULT '',
 attempted_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'guest' CHECK(state IN ('guest','exported','archived'))
);

CREATE TABLE network_defaults (
 id INTEGER PRIMARY KEY CHECK(id=1),
 revision TEXT NOT NULL,
 policy TEXT NOT NULL,
 accepted_at TEXT NOT NULL
);

CREATE TABLE notification_reads (
 notification_id TEXT NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
 actor TEXT NOT NULL REFERENCES accounts(id),
 PRIMARY KEY(notification_id,actor)
);

CREATE TABLE notification_policy (
 id INTEGER PRIMARY KEY CHECK(id=1),
 agent_run_outcome INTEGER NOT NULL CHECK(agent_run_outcome IN (0,1)),
 conversation_shared INTEGER NOT NULL CHECK(conversation_shared IN (0,1)),
 operator_directed INTEGER NOT NULL CHECK(operator_directed IN (0,1)),
 operator_broadcast INTEGER NOT NULL CHECK(operator_broadcast IN (0,1))
);

INSERT INTO notification_policy(id,agent_run_outcome,conversation_shared,operator_directed,operator_broadcast) VALUES(1,0,1,1,1);

CREATE TABLE notifications (
 id TEXT PRIMARY KEY,
 audience TEXT REFERENCES accounts(id),
 project_id TEXT REFERENCES projects(id),
 session_id TEXT REFERENCES sessions(id),
 title TEXT NOT NULL,
 body TEXT NOT NULL,
 created_at TEXT NOT NULL
);

CREATE TABLE project_activity (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 session_id TEXT,
 terminal_id TEXT,
 kind TEXT NOT NULL CHECK(kind IN ('agent_start','agent_control','terminal_start','terminal_control','files','capture','descendants')),
 state TEXT NOT NULL CHECK(state IN ('active','unknown','complete')),
 detail TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK(session_id IS NULL OR terminal_id IS NULL),
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 FOREIGN KEY(project_id,terminal_id) REFERENCES terminals(project_id,id)
);

CREATE TABLE project_event_frontiers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 retained_after INTEGER NOT NULL DEFAULT 0
, event_count INTEGER NOT NULL DEFAULT 0);

CREATE TABLE project_operations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 kind TEXT NOT NULL CHECK(kind IN ('create','pause','resume','delete')),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','issued','unknown','succeeded')),
 phase TEXT NOT NULL,
 request TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 attempted_at TEXT NOT NULL
);

CREATE TABLE project_resources (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 store_id TEXT NOT NULL CHECK(store_id<>''),
 profile TEXT NOT NULL,
 workspace_volume TEXT NOT NULL DEFAULT '',
 runtime_volume TEXT NOT NULL DEFAULT '',
 sandbox_id TEXT NOT NULL DEFAULT '',
 mutation_revision TEXT NOT NULL DEFAULT '',
 epoch INTEGER NOT NULL DEFAULT 0 CHECK(epoch>=0),
 observed_at TEXT NOT NULL DEFAULT '',
 occupied INTEGER NOT NULL CHECK(occupied IN (0,1))
, storage_key TEXT NOT NULL DEFAULT '');

CREATE TABLE projects (
 id TEXT PRIMARY KEY CHECK(id<>''),
 owner_id TEXT NOT NULL REFERENCES accounts(id) CHECK(owner_id<>''),
 title TEXT NOT NULL,
 creation_digest TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'created' CHECK(state IN ('created','provisioning','active','pausing','paused','resuming','blocked','deleting','deleted')),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 activity_revision INTEGER NOT NULL DEFAULT 0 CHECK(activity_revision>=0),
 idle_deadline TEXT,
 failure TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

CREATE TABLE release_coordinators (
 owner TEXT PRIMARY KEY CHECK(owner<>''),
 generation INTEGER NOT NULL UNIQUE CHECK(generation>0)
);

CREATE TABLE schedule_catalog (
    owner_id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0)
);

CREATE TABLE schedule_runs (
 id TEXT PRIMARY KEY,
 schedule_id TEXT NOT NULL REFERENCES schedules(id),
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 due_at TEXT NOT NULL,
 schedule_revision INTEGER NOT NULL CHECK(schedule_revision>0),
 notification_policy TEXT NOT NULL DEFAULT 'each_run' CHECK(notification_policy IN ('each_run','on_finish')),
 notify INTEGER NOT NULL CHECK(notify IN (0,1)),
 created_at TEXT NOT NULL,
 UNIQUE(schedule_id,request_id),
 FOREIGN KEY(project_id,session_id,turn_id) REFERENCES turns(project_id,session_id,id)
);

CREATE TABLE schedules (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 session_id TEXT,
 title TEXT NOT NULL,
 prompt TEXT NOT NULL,
 cron TEXT NOT NULL,
 timezone TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','paused','exhausted','canceled','deleted')),
 trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('at','cron','interval','manual')),
 run_at TEXT,
 expires_at TEXT,
 end_reason TEXT,
 notify INTEGER NOT NULL DEFAULT 1 CHECK(notify IN (0,1)),
 retry_at TEXT,
 retry_count INTEGER NOT NULL DEFAULT 0 CHECK(retry_count BETWEEN 0 AND 3),
 last_error TEXT NOT NULL DEFAULT '' CHECK(length(last_error)<=256),
 origin_session_id TEXT,
 origin_run_id TEXT,
 origin_tool_call_id TEXT,
 max_occurrences INTEGER CHECK(max_occurrences IS NULL OR max_occurrences BETWEEN 1 AND 100),
 admitted_count INTEGER NOT NULL DEFAULT 0 CHECK(admitted_count>=0),
 starts_at TEXT,
 completed_by_run_id TEXT,
 notification_policy TEXT NOT NULL DEFAULT 'each_run' CHECK(notification_policy IN ('each_run','on_finish')),
 next_at TEXT,
 revision INTEGER NOT NULL DEFAULT 1,
 creation_digest TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((trigger_kind='at' AND run_at IS NOT NULL AND cron='') OR (trigger_kind IN ('cron','interval') AND run_at IS NULL AND cron<>'') OR (trigger_kind='manual' AND run_at IS NULL AND cron='')),
 CHECK(state='active' OR next_at IS NULL),
 CHECK(max_occurrences IS NULL OR (trigger_kind='interval' AND expires_at IS NOT NULL AND (starts_at IS NULL OR starts_at<expires_at))),
 CHECK(starts_at IS NULL OR (max_occurrences IS NOT NULL AND starts_at<>'')),
 CHECK(notification_policy='each_run' OR max_occurrences IS NOT NULL),
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 FOREIGN KEY(project_id,origin_session_id) REFERENCES sessions(project_id,id)
);

CREATE TABLE schedule_mutation_receipts (
 project_id TEXT NOT NULL REFERENCES projects(id),
 request_id TEXT NOT NULL,
 digest TEXT NOT NULL,
 schedule_id TEXT NOT NULL REFERENCES schedules(id),
 result TEXT NOT NULL CHECK(length(result)<=2097152),
 origin_session_id TEXT,
 origin_run_id TEXT,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,request_id)
);
CREATE INDEX schedules_source ON schedules(project_id,origin_session_id,id);

CREATE TABLE service_gateways (
 audience TEXT PRIMARY KEY CHECK(audience<>''),
 instance TEXT NOT NULL CHECK(instance<>''),
 generation INTEGER NOT NULL CHECK(generation>0),
 slots INTEGER NOT NULL CHECK(slots BETWEEN 1 AND 256)
);

CREATE TABLE service_grants (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 incarnation_id TEXT NOT NULL,
 audience TEXT NOT NULL CHECK(audience<>''),
 credential_digest TEXT NOT NULL CHECK(length(credential_digest)=64),
 expires_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','revoked','expired')),
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 FOREIGN KEY(project_id,session_id,incarnation_id) REFERENCES agent_incarnations(project_id,session_id,id)
);

CREATE TABLE service_request_slots (
 audience TEXT NOT NULL REFERENCES service_gateways(audience),
 slot INTEGER NOT NULL CHECK(slot BETWEEN 0 AND 255),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 closed INTEGER NOT NULL CHECK(closed IN (0,1)),
 grant_id TEXT NOT NULL DEFAULT '',
 turn_id TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '',
 run_owner INTEGER NOT NULL DEFAULT 0 CHECK(run_owner>=0),
 PRIMARY KEY(audience,slot)
);

CREATE TABLE session_cleanup (
 session_id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL,
 handle TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','complete')),
 attempted_at TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id)
);

CREATE TABLE sessions (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL REFERENCES projects(id),
 title TEXT NOT NULL,
 title_source TEXT NOT NULL DEFAULT 'user' CHECK(title_source IN ('user','pending','automatic')),
 creation_digest TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'idle' CHECK(state IN ('idle','starting','running','stopping','blocked')),
 deletion_intent INTEGER NOT NULL DEFAULT 0 CHECK(deletion_intent IN (0,1,2)),
 control_revision INTEGER NOT NULL DEFAULT 1 CHECK(control_revision>0),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 failure TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(project_id,id)
);

CREATE TABLE share_files (
 share_id TEXT NOT NULL REFERENCES shares(id),
 path TEXT NOT NULL,
 digest TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>=0),
 PRIMARY KEY(share_id,path)
);

CREATE TABLE shares (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 token_digest TEXT NOT NULL UNIQUE,
 title TEXT NOT NULL,
 frontier INTEGER NOT NULL,
 paths TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','published','revoked','failed')),
 expires_at TEXT NOT NULL,
 cleaned INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id)
);

CREATE TABLE terminal_incarnations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 terminal_id TEXT NOT NULL,
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 handle TEXT NOT NULL,
 owner INTEGER NOT NULL CHECK(owner>0),
 state TEXT NOT NULL CHECK(state IN ('starting','running','exited','lost')),
 rows INTEGER NOT NULL CHECK(rows BETWEEN 1 AND 500),
 cols INTEGER NOT NULL CHECK(cols BETWEEN 1 AND 1000),
 exit_code INTEGER,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,terminal_id) REFERENCES terminals(project_id,id),
 UNIQUE(project_id,terminal_id,id),
 UNIQUE(store_id,sandbox_id,epoch,handle),
 CHECK((state IN ('exited') AND exit_code IS NOT NULL) OR (state NOT IN ('exited') AND exit_code IS NULL))
);

CREATE TABLE terminals (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL REFERENCES projects(id),
 title TEXT NOT NULL,
 creation_digest TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'created' CHECK(state IN ('created','starting','running','exited','lost')),
 deletion_intent INTEGER NOT NULL DEFAULT 0 CHECK(deletion_intent IN (0,1,2)),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 rows INTEGER NOT NULL CHECK(rows BETWEEN 1 AND 500),
 cols INTEGER NOT NULL CHECK(cols BETWEEN 1 AND 1000),
 exit_code INTEGER,
 failure TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(project_id,id)
);

CREATE TABLE turns (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 request_id TEXT NOT NULL CHECK(request_id<>''),
 content TEXT NOT NULL CHECK(length(content)>0),
 initially_held INTEGER NOT NULL CHECK(initially_held IN (0,1)),
 prompt_revision INTEGER NOT NULL DEFAULT 1 CHECK(prompt_revision>0),
 queue_policy TEXT NOT NULL DEFAULT 'released' CHECK(queue_policy IN ('held','released')),
 disposition TEXT NOT NULL DEFAULT 'pending' CHECK(disposition IN ('pending','claimed','consumed','handled','canceled')),
 dispatch_error TEXT NOT NULL DEFAULT '',
 sequence INTEGER NOT NULL CHECK(sequence>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 UNIQUE(session_id,request_id),
 UNIQUE(session_id,sequence),
 UNIQUE(project_id,session_id,id)
);

-- Prompt intent, execution, native delivery and mutation receipt are separate
-- authorities. Prompt status is derived at read time from policy, disposition
-- and correlated run evidence.
-- Execution guards use runs; admission uses policy/disposition and claims.
CREATE TABLE execution_runs (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 prompt_id TEXT NOT NULL,
 incarnation_id TEXT NOT NULL,
 owner INTEGER NOT NULL CHECK(owner>0),
 state TEXT NOT NULL CHECK(state IN ('starting','running','stopping','settled','failed','unknown')),
 acknowledged INTEGER NOT NULL DEFAULT 0 CHECK(acknowledged IN (0,1)),
 reconciled_at TEXT NOT NULL DEFAULT '',
 outcome TEXT NOT NULL DEFAULT '' CHECK(outcome IN ('','completed','failed','canceled','unknown')),
 started_at TEXT NOT NULL,
 settled_at TEXT NOT NULL DEFAULT '',
 elapsed_ms INTEGER CHECK(elapsed_ms>=0),
 FOREIGN KEY(project_id,session_id,prompt_id) REFERENCES turns(project_id,session_id,id),
 FOREIGN KEY(project_id,session_id,incarnation_id) REFERENCES agent_incarnations(project_id,session_id,id),
 UNIQUE(session_id,id)
);

CREATE TABLE input_deliveries (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 prompt_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 command_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 input_token TEXT NOT NULL UNIQUE,
 channel TEXT NOT NULL CHECK(channel IN ('normal','steer')),
 state TEXT NOT NULL CHECK(state IN ('accepted','dispatching','native_queued','consumed','handled','rejected','removed','unknown')),
 acceptance_sequence INTEGER NOT NULL CHECK(acceptance_sequence>0),
 consumed_sequence INTEGER NOT NULL DEFAULT 0 CHECK(consumed_sequence>=0),
 consumed_elapsed_ms INTEGER CHECK(consumed_elapsed_ms>=0),
 accepted_at TEXT NOT NULL,
 ack_at TEXT NOT NULL DEFAULT '',
 consumed_at TEXT NOT NULL DEFAULT '',
 handled_at TEXT NOT NULL DEFAULT '',
 removed_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(project_id,session_id,prompt_id) REFERENCES turns(project_id,session_id,id),
 FOREIGN KEY(command_id) REFERENCES agent_commands(id),
 FOREIGN KEY(session_id,run_id) REFERENCES execution_runs(session_id,id),
 UNIQUE(command_id),
 UNIQUE(session_id,request_id),
 UNIQUE(session_id,acceptance_sequence)
);

CREATE TABLE control_receipts (
 request_id TEXT NOT NULL CHECK(request_id<>''),
 project_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('enqueue','hold','release','cancel','steer','stop')),
 intent_digest TEXT NOT NULL,
 prompt_id TEXT NOT NULL DEFAULT '',
 delivery_id TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('accepted','rejected','completed','unknown')),
 result BLOB NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 PRIMARY KEY(session_id,request_id)
);

CREATE TABLE workspace_readers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 handle TEXT NOT NULL UNIQUE,
 created_at TEXT NOT NULL
);

CREATE INDEX agent_commands_pending ON agent_commands(incarnation_id,state,created_at);

CREATE INDEX execution_runs_live ON execution_runs(session_id,state) WHERE state IN ('starting','running','stopping','unknown');
CREATE UNIQUE INDEX execution_runs_one_live ON execution_runs(session_id) WHERE state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0);
CREATE UNIQUE INDEX input_deliveries_one_claim ON input_deliveries(prompt_id) WHERE state IN ('accepted','dispatching','native_queued','consumed','handled','unknown');
CREATE INDEX input_deliveries_run ON input_deliveries(session_id,run_id,acceptance_sequence);
CREATE INDEX input_deliveries_prompt ON input_deliveries(prompt_id,acceptance_sequence DESC);
CREATE INDEX input_deliveries_pending ON input_deliveries(session_id,state) WHERE state IN ('accepted','dispatching','native_queued','unknown');
CREATE INDEX control_receipts_session ON control_receipts(session_id,updated_at);

CREATE UNIQUE INDEX agent_incarnation_live ON agent_incarnations(session_id) WHERE state IN ('starting','running','stopping','unknown');

CREATE INDEX agent_prompt_turn ON agent_commands(turn_id) WHERE kind='prompt';

CREATE INDEX auth_sessions_expiry ON auth_sessions(expires_at);

CREATE INDEX conversation_changes_detail ON conversation_changes(session_id,item_id,revision);

CREATE INDEX conversation_items_message ON conversation_items(session_id,message_id);

CREATE INDEX conversation_items_order ON conversation_items(session_id,position,block_index,id);

CREATE INDEX conversation_items_source ON conversation_items(session_id,source_key);

CREATE INDEX conversation_items_turn ON conversation_items(session_id,turn_id,position,block_index,id);

CREATE INDEX conversation_retention_pressure ON conversation_retention(change_bytes,session_id);

CREATE INDEX conversation_sources_session ON conversation_sources(session_id,source_id);

CREATE INDEX conversation_turns_order ON conversation_turns(session_id,sequence);

CREATE INDEX events_project ON events(project_id,id);

CREATE INDEX file_operations_pending ON file_operations(settled,updated_at,project_id);

CREATE INDEX messages_project ON messages(project_id,session_id,sequence);

CREATE INDEX messages_turn_final ON messages(session_id,turn_id,sequence DESC) WHERE role='assistant';

CREATE INDEX notifications_audience ON notifications(audience,created_at,id);

CREATE INDEX project_activity_pending ON project_activity(project_id,state);

CREATE INDEX project_event_pressure ON project_event_frontiers(event_count,project_id);

CREATE UNIQUE INDEX project_operation_lane ON project_operations(project_id) WHERE state<>'succeeded';

CREATE INDEX project_operations_pending ON project_operations(state,attempted_at,project_id);

CREATE UNIQUE INDEX project_runtime_unique ON project_resources(store_id,runtime_volume) WHERE runtime_volume<>'';

CREATE UNIQUE INDEX project_sandbox_unique ON project_resources(store_id,sandbox_id) WHERE sandbox_id<>'';

CREATE UNIQUE INDEX project_workspace_unique ON project_resources(store_id,workspace_volume) WHERE workspace_volume<>'';

CREATE INDEX projects_owner ON projects(owner_id,created_at,id);

CREATE INDEX release_activity_pending ON project_activity(id) WHERE state<>'complete';

CREATE INDEX release_agent_transition_pending ON agent_incarnations(id) WHERE state IN ('starting','stopping','unknown') OR recovery_state IN ('pending','replaying','failed');

CREATE INDEX release_command_pending ON agent_commands(id) WHERE state IN ('pending','sending','dispatching','unknown');

CREATE INDEX release_content_publication_pending ON content_publications(revision_digest) WHERE stage NOT IN ('published','failed');

CREATE INDEX release_file_write_pending ON file_operations(id) WHERE settled=0;

CREATE INDEX release_model_request_pending ON service_request_slots(audience,slot) WHERE closed=0;

CREATE INDEX release_native_capture_pending ON native_captures(path) WHERE state='guest';

CREATE INDEX release_project_operation_pending ON project_operations(id) WHERE state<>'succeeded';

CREATE INDEX release_project_transition_pending ON projects(id) WHERE state IN ('provisioning','resuming','pausing','deleting');

CREATE INDEX release_session_cleanup_pending ON sessions(id) WHERE deletion_intent=1;

CREATE INDEX release_share_publication_pending ON shares(id) WHERE state='pending' OR (state IN ('revoked','failed') AND cleaned=0);

CREATE INDEX release_terminal_cleanup_pending ON terminals(id) WHERE deletion_intent=1;

CREATE INDEX release_terminal_start_pending ON terminal_incarnations(id) WHERE state='starting';

CREATE INDEX release_run_pending ON execution_runs(prompt_id) WHERE state IN ('starting','running','stopping','unknown');

CREATE INDEX schedule_runs_project ON schedule_runs(project_id,created_at,id);
CREATE UNIQUE INDEX schedule_runs_prompt ON schedule_runs(turn_id);
CREATE INDEX scheduled_input_authority ON input_deliveries(run_id,state,prompt_id);
CREATE INDEX schedule_runs_latest ON schedule_runs(schedule_id,created_at DESC,id DESC);

CREATE INDEX schedules_due ON schedules(state,retry_at,next_at,expires_at,project_id);

CREATE INDEX schedules_overview_order ON schedules(
    state,
    CASE WHEN state<>'active' THEN 2 WHEN trigger_kind='manual' THEN 1 ELSE 0 END,
    coalesce(next_at,''),id
);

CREATE INDEX service_grants_live ON service_grants(project_id,state,expires_at);

CREATE UNIQUE INDEX service_grants_one_live ON service_grants(incarnation_id,audience) WHERE state='active';

CREATE INDEX sessions_project ON sessions(project_id,created_at,id);

CREATE INDEX shares_cleanup ON shares(state,cleaned,created_at);

CREATE UNIQUE INDEX terminal_incarnation_live ON terminal_incarnations(terminal_id) WHERE state IN ('starting','running');

CREATE INDEX terminal_incarnations_project ON terminal_incarnations(project_id,state);

CREATE INDEX terminals_project ON terminals(project_id,created_at,id);

CREATE INDEX turns_pending ON turns(project_id,disposition,queue_policy,session_id,sequence);

CREATE VIEW owner_handoff_blockers AS
SELECT b.kind,b.id FROM release_blockers b WHERE
 (b.kind NOT IN ('turn','model_request'))
 OR (b.kind='turn' AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t WHERE t.id=b.id))
 OR (b.kind='model_request' AND NOT EXISTS(
  SELECT 1 FROM service_request_slots r JOIN owner_transferable_turns t ON t.id=r.turn_id AND t.run_id=r.run_id
  JOIN service_grants g ON g.id=r.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=r.audience
   AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now')
  WHERE r.audience||':'||r.slot=b.id AND r.closed=0
 ));

CREATE VIEW owner_transferable_turns AS
SELECT r.prompt_id AS id,r.incarnation_id,r.id AS run_id FROM execution_runs r
JOIN agent_commands c ON c.id=r.id AND c.state='applied'
JOIN agent_incarnations i ON i.id=r.incarnation_id AND i.session_id=r.session_id AND i.state='running'
JOIN sessions s ON s.id=r.session_id AND s.deletion_intent=0 AND s.state='running'
JOIN projects p ON p.id=r.project_id AND p.state='active'
WHERE r.state='running';

CREATE VIEW release_blockers AS
SELECT 'turn' AS kind,r.prompt_id AS id FROM execution_runs r WHERE r.state IN ('starting','running','stopping') OR (r.state='unknown' AND r.acknowledged=0)
UNION ALL SELECT 'delivery',d.id FROM input_deliveries d JOIN execution_runs r ON r.id=d.run_id WHERE d.state IN ('accepted','dispatching','native_queued','unknown') AND r.acknowledged=0
UNION ALL SELECT 'command' AS kind,id AS id FROM agent_commands INDEXED BY release_command_pending
WHERE state IN ('pending','sending','dispatching','unknown') AND (state<>'unknown' OR NOT EXISTS(
 SELECT 1 FROM execution_runs r JOIN agent_incarnations i ON i.id=agent_commands.incarnation_id
 WHERE r.id=coalesce((SELECT run_id FROM input_deliveries WHERE command_id=agent_commands.id),json_extract(agent_commands.payload,'$.run_id')) AND r.incarnation_id=i.id AND r.acknowledged=1 AND i.state='exited'
 AND i.cleanup_state='settled' AND i.recovery_state NOT IN ('pending','replaying')
))
UNION ALL SELECT 'activity' AS kind,id AS id FROM project_activity INDEXED BY release_activity_pending WHERE state<>'complete'
UNION ALL SELECT 'agent_transition' AS kind,id AS id FROM agent_incarnations INDEXED BY release_agent_transition_pending
WHERE (state IN ('starting','stopping','unknown') OR recovery_state IN ('pending','replaying','failed'))
 AND (state IN ('starting','stopping','unknown') OR recovery_state<>'failed' OR EXISTS(
 SELECT 1 FROM execution_runs r WHERE r.incarnation_id=agent_incarnations.id AND r.state='unknown' AND r.acknowledged=0
))
UNION ALL SELECT 'terminal_start' AS kind,id AS id FROM terminal_incarnations INDEXED BY release_terminal_start_pending WHERE state='starting'
UNION ALL SELECT 'project_operation' AS kind,id AS id FROM project_operations INDEXED BY release_project_operation_pending WHERE state<>'succeeded'
UNION ALL SELECT 'project_transition' AS kind,id AS id FROM projects INDEXED BY release_project_transition_pending WHERE state IN ('provisioning','resuming','pausing','deleting')
UNION ALL SELECT 'file_write' AS kind,id AS id FROM file_operations INDEXED BY release_file_write_pending WHERE settled=0
UNION ALL SELECT 'session_cleanup' AS kind,id AS id FROM sessions INDEXED BY release_session_cleanup_pending WHERE deletion_intent=1
UNION ALL SELECT 'terminal_cleanup' AS kind,id AS id FROM terminals INDEXED BY release_terminal_cleanup_pending WHERE deletion_intent=1
UNION ALL SELECT 'content_publication' AS kind,revision_digest AS id FROM content_publications INDEXED BY release_content_publication_pending WHERE stage NOT IN ('published','failed')
UNION ALL SELECT 'native_capture' AS kind,path AS id FROM native_captures INDEXED BY release_native_capture_pending WHERE state='guest'
UNION ALL SELECT 'share_publication' AS kind,id AS id FROM shares INDEXED BY release_share_publication_pending WHERE state='pending' OR (state IN ('revoked','failed') AND cleaned=0)
UNION ALL SELECT 'model_request' AS kind,audience||':'||slot AS id FROM service_request_slots INDEXED BY release_model_request_pending WHERE closed=0;

CREATE TRIGGER command_control_insert AFTER INSERT ON agent_commands BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

CREATE TRIGGER command_control_update AFTER UPDATE OF state ON agent_commands WHEN NEW.state<>OLD.state BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

CREATE TRIGGER conversation_change_account_delete AFTER DELETE ON conversation_changes BEGIN
 UPDATE conversation_retention SET change_count=change_count-1,change_bytes=change_bytes-OLD.retained_bytes,
 pruned_ordinal=CASE WHEN OLD.revision>pruned_revision THEN OLD.ordinal WHEN OLD.revision=pruned_revision THEN max(pruned_ordinal,OLD.ordinal) ELSE pruned_ordinal END,
 pruned_revision=max(pruned_revision,OLD.revision) WHERE session_id=OLD.session_id;
END;

CREATE TRIGGER conversation_change_account_insert AFTER INSERT ON conversation_changes BEGIN
 UPDATE conversation_changes SET retained_bytes=length(CAST(NEW.data AS BLOB))+length(NEW.item_id)+128+
 coalesce((SELECT length(CAST(content AS BLOB)) FROM conversation_details WHERE session_id=NEW.session_id AND item_id=NEW.item_id AND item_revision=NEW.revision),0)
 WHERE session_id=NEW.session_id AND revision=NEW.revision AND ordinal=NEW.ordinal;
 INSERT INTO conversation_retention(session_id,change_count,change_bytes)
 SELECT NEW.session_id,1,retained_bytes FROM conversation_changes WHERE session_id=NEW.session_id AND revision=NEW.revision AND ordinal=NEW.ordinal
 ON CONFLICT(session_id) DO UPDATE SET change_count=change_count+1,change_bytes=change_bytes+excluded.change_bytes;
END;

CREATE TRIGGER conversation_detail_after_change AFTER DELETE ON conversation_changes BEGIN
 INSERT OR IGNORE INTO conversation_detail_gc SELECT OLD.session_id,OLD.item_id,OLD.revision
 WHERE EXISTS(SELECT 1 FROM conversation_details WHERE session_id=OLD.session_id AND item_id=OLD.item_id AND item_revision=OLD.revision);
END;

CREATE TRIGGER conversation_detail_after_item AFTER DELETE ON conversation_items BEGIN
 INSERT OR IGNORE INTO conversation_detail_gc SELECT OLD.session_id,OLD.id,OLD.item_revision
 WHERE EXISTS(SELECT 1 FROM conversation_details WHERE session_id=OLD.session_id AND item_id=OLD.id AND item_revision=OLD.item_revision);
END;

CREATE TRIGGER conversation_detail_after_update AFTER UPDATE OF item_revision ON conversation_items BEGIN
 INSERT OR IGNORE INTO conversation_detail_gc SELECT OLD.session_id,OLD.id,OLD.item_revision
 WHERE EXISTS(SELECT 1 FROM conversation_details WHERE session_id=OLD.session_id AND item_id=OLD.id AND item_revision=OLD.item_revision);
END;

CREATE TRIGGER conversation_message_insert AFTER INSERT ON messages BEGIN
 INSERT OR IGNORE INTO conversation_dirty_turns VALUES(NEW.session_id,NEW.turn_id);
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;

CREATE TRIGGER conversation_session_insert AFTER INSERT ON sessions BEGIN
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.id);
END;

CREATE TRIGGER conversation_source_ingest AFTER INSERT ON agent_records BEGIN
 INSERT INTO conversation_sources(session_id,incarnation_id,sequence)
 SELECT session_id,NEW.incarnation_id,NEW.sequence FROM agent_incarnations WHERE id=NEW.incarnation_id;
END;

CREATE TRIGGER conversation_source_pending AFTER INSERT ON conversation_sources BEGIN
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;

CREATE TRIGGER conversation_turn_insert AFTER INSERT ON turns BEGIN
 INSERT OR IGNORE INTO conversation_dirty_turns VALUES(NEW.session_id,NEW.id);
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;

CREATE TRIGGER conversation_turn_update AFTER UPDATE ON turns BEGIN
 INSERT OR IGNORE INTO conversation_dirty_turns VALUES(NEW.session_id,NEW.id);
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;

CREATE TRIGGER index_message_delete AFTER DELETE ON messages BEGIN
 INSERT INTO message_search(message_search,rowid,content) VALUES('delete',OLD.rowid,OLD.content);
END;

CREATE TRIGGER index_message_insert AFTER INSERT ON messages BEGIN
 INSERT INTO message_search(rowid,content) VALUES(NEW.rowid,NEW.content);
END;

CREATE TRIGGER notify_run_outcome AFTER UPDATE OF state,outcome ON execution_runs
WHEN (NEW.state='settled' OR NEW.state IN ('failed','unknown')) AND (NEW.state<>OLD.state OR NEW.outcome<>OLD.outcome)
BEGIN
 DELETE FROM notification_reads WHERE notification_id IN
  (SELECT n.id FROM notifications n JOIN turns t ON n.id='turn/'||t.id
   WHERE (t.id=NEW.prompt_id OR EXISTS(SELECT 1 FROM input_deliveries d WHERE d.prompt_id=t.id AND d.run_id=NEW.id AND d.state='consumed'))
   AND (
 (NOT EXISTS(SELECT 1 FROM schedule_runs sr WHERE sr.turn_id=t.id AND sr.notification_policy='on_finish')
  AND ((SELECT agent_run_outcome FROM notification_policy WHERE id=1)=1 OR EXISTS(SELECT 1 FROM schedule_runs sr WHERE sr.turn_id=t.id AND sr.notify=1)))
 OR EXISTS(SELECT 1 FROM schedule_runs sr JOIN schedules task ON task.id=sr.schedule_id WHERE sr.turn_id=t.id AND sr.notification_policy='on_finish' AND sr.notify=1
 AND (NEW.state IN ('failed','unknown') OR NEW.outcome IN ('failed','unknown','canceled') OR task.completed_by_run_id=NEW.id OR EXISTS(SELECT 1 FROM notifications existing WHERE existing.id='turn/'||t.id))))
   AND n.title<>'Agent turn '||CASE NEW.state WHEN 'settled' THEN NEW.outcome ELSE NEW.state END);
 INSERT INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT 'turn/'||t.id,p.owner_id,NEW.project_id,NEW.session_id,'Agent turn '||CASE NEW.state WHEN 'settled' THEN NEW.outcome ELSE NEW.state END,'Open the conversation to inspect the outcome.',coalesce(nullif(NEW.settled_at,''),NEW.started_at)
 FROM turns t JOIN projects p ON p.id=NEW.project_id AND p.state NOT IN ('deleting','deleted')
 WHERE (t.id=NEW.prompt_id OR EXISTS(SELECT 1 FROM input_deliveries d WHERE d.prompt_id=t.id AND d.run_id=NEW.id AND d.state='consumed'))
 AND (
 (NOT EXISTS(SELECT 1 FROM schedule_runs sr WHERE sr.turn_id=t.id AND sr.notification_policy='on_finish')
  AND ((SELECT agent_run_outcome FROM notification_policy WHERE id=1)=1 OR EXISTS(SELECT 1 FROM schedule_runs sr WHERE sr.turn_id=t.id AND sr.notify=1)))
 OR EXISTS(SELECT 1 FROM schedule_runs sr JOIN schedules task ON task.id=sr.schedule_id WHERE sr.turn_id=t.id AND sr.notification_policy='on_finish' AND sr.notify=1
 AND (NEW.state IN ('failed','unknown') OR NEW.outcome IN ('failed','unknown','canceled') OR task.completed_by_run_id=NEW.id OR EXISTS(SELECT 1 FROM notifications existing WHERE existing.id='turn/'||t.id))))
 ON CONFLICT(id) DO UPDATE SET title=excluded.title,body=excluded.body,created_at=excluded.created_at WHERE notifications.title<>excluded.title;
END;

CREATE TRIGGER notify_scheduled_disposition AFTER UPDATE OF disposition ON turns
WHEN NEW.disposition IN ('canceled','handled') AND NEW.disposition<>OLD.disposition
BEGIN
 INSERT INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT 'turn/'||NEW.id,p.owner_id,NEW.project_id,NEW.session_id,'Scheduled input '||NEW.disposition,'Open the conversation to inspect the input.',NEW.updated_at
 FROM projects p WHERE p.id=NEW.project_id AND p.state NOT IN ('deleting','deleted')
 AND EXISTS(SELECT 1 FROM schedule_runs sr WHERE sr.turn_id=NEW.id AND sr.notify=1)
 ON CONFLICT(id) DO UPDATE SET title=excluded.title,body=excluded.body,created_at=excluded.created_at;
 DELETE FROM notification_reads WHERE notification_id='turn/'||NEW.id;
END;

CREATE TRIGGER project_event_account_delete AFTER DELETE ON events BEGIN
 UPDATE project_event_frontiers SET event_count=event_count-1 WHERE project_id=OLD.project_id;
END;

CREATE TRIGGER project_event_account_insert AFTER INSERT ON events BEGIN
 INSERT INTO project_event_frontiers(project_id,retained_after,event_count) VALUES(NEW.project_id,0,1)
 ON CONFLICT(project_id) DO UPDATE SET event_count=event_count+1;
END;

CREATE TRIGGER project_event_pruned BEFORE DELETE ON events BEGIN
 INSERT INTO project_event_frontiers(project_id,retained_after) VALUES(OLD.project_id,OLD.id)
 ON CONFLICT(project_id) DO UPDATE SET retained_after=max(retained_after,excluded.retained_after);
END;

CREATE TRIGGER release_activity_start BEFORE INSERT ON project_activity
WHEN NEW.kind IN ('agent_start','terminal_start','files') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_agent_start BEFORE INSERT ON agent_incarnations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_command_insert BEFORE INSERT ON agent_commands
WHEN NEW.kind IN ('prompt','steer') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_command_send BEFORE UPDATE OF state ON agent_commands
WHEN NEW.kind IN ('prompt','steer') AND OLD.state='pending' AND NEW.state='sending'
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_file_write BEFORE INSERT ON file_operations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_model_request_insert BEFORE INSERT ON service_request_slots
WHEN NEW.closed=0 AND (SELECT phase FROM application_release WHERE id=1) NOT IN ('open','draining')
 AND NOT EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase IN ('ready','activating'))
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_model_request_update BEFORE UPDATE ON service_request_slots
WHEN NEW.closed=0 AND (SELECT phase FROM application_release WHERE id=1) NOT IN ('open','draining')
 AND NOT EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase IN ('ready','activating'))
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_owner_grant_changed AFTER UPDATE OF state,expires_at ON service_grants
WHEN (NEW.state<>'active' OR NEW.expires_at<=strftime('%Y-%m-%dT%H:%M:%f000000Z','now'))
 AND EXISTS(SELECT 1 FROM service_request_slots WHERE grant_id=NEW.id AND closed=0)
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_prompt_changed AFTER UPDATE OF state ON agent_commands
WHEN NEW.kind='prompt' AND OLD.state='applied' AND NEW.state<>'applied'
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_request_insert AFTER INSERT ON service_request_slots
WHEN NEW.closed=0
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
 AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t JOIN service_grants g ON g.id=NEW.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=NEW.audience AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE t.id=NEW.turn_id AND t.run_id=NEW.run_id)
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_request_update AFTER UPDATE ON service_request_slots
WHEN NEW.closed=0
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
 AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t JOIN service_grants g ON g.id=NEW.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=NEW.audience AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE t.id=NEW.turn_id AND t.run_id=NEW.run_id)
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_session_changed AFTER UPDATE OF state ON sessions
WHEN OLD.state='running' AND NEW.state<>'running'
 AND EXISTS(SELECT 1 FROM execution_runs WHERE session_id=NEW.id AND state='running')
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_turn_finished AFTER UPDATE OF state ON execution_runs
WHEN OLD.state='running' AND NEW.state<>'running'
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_project_start BEFORE INSERT ON project_operations
WHEN NEW.kind IN ('create','resume') AND (SELECT phase FROM application_release WHERE id=1)<>'open'
 AND NOT(NEW.kind='resume' AND EXISTS(
 SELECT 1 FROM application_release WHERE id=1 AND phase='activating' AND activation_instance<>'' AND current_instance<>activation_instance
 ) AND EXISTS(SELECT 1 FROM agent_incarnations WHERE project_id=NEW.project_id AND state='exited' AND recovery_state IN ('pending','replaying')))
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_publication_start BEFORE INSERT ON content_publications
WHEN NOT EXISTS(SELECT 1 FROM content_publications WHERE store_id=NEW.store_id AND purpose=NEW.purpose AND revision_digest=NEW.revision_digest)
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
 AND NOT EXISTS(SELECT 1 FROM application_release WHERE id=1 AND phase='activating' AND activation_instance<>'' AND current_instance<>activation_instance)
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_ready_activity_insert AFTER INSERT ON project_activity
WHEN (NEW.state<>'complete') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_activity_update AFTER UPDATE OF state ON project_activity
WHEN (NEW.state<>'complete') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_agent_transition_insert AFTER INSERT ON agent_incarnations
WHEN (NEW.state IN ('starting','stopping','unknown') OR NEW.recovery_state IN ('pending','replaying','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_agent_transition_update AFTER UPDATE OF state,recovery_state ON agent_incarnations
WHEN (NEW.state IN ('starting','stopping','unknown') OR NEW.recovery_state IN ('pending','replaying','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_cleanup_loss AFTER UPDATE OF cleanup_state ON agent_incarnations
WHEN NEW.cleanup_state='unknown' AND OLD.cleanup_state='settled'
 AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_command_insert AFTER INSERT ON agent_commands
WHEN (NEW.state IN ('pending','sending','dispatching','unknown')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_command_update AFTER UPDATE OF state ON agent_commands
WHEN (NEW.state IN ('pending','sending','dispatching','unknown')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_content_publication_insert AFTER INSERT ON content_publications
WHEN (NEW.stage NOT IN ('published','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_content_publication_update AFTER UPDATE OF stage ON content_publications
WHEN (NEW.stage NOT IN ('published','failed')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_file_write_insert AFTER INSERT ON file_operations
WHEN (NEW.settled=0) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_file_write_update AFTER UPDATE OF settled ON file_operations
WHEN (NEW.settled=0) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_gateway_change AFTER UPDATE OF instance,generation,slots ON service_gateways
WHEN (NEW.instance<>OLD.instance OR NEW.generation<>OLD.generation OR NEW.slots<>OLD.slots) AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='gateway_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_native_capture_insert AFTER INSERT ON native_captures
WHEN (NEW.state='guest') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_native_capture_update AFTER UPDATE OF state ON native_captures
WHEN (NEW.state='guest') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_project_operation_insert AFTER INSERT ON project_operations
WHEN (NEW.state<>'succeeded') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_project_operation_update AFTER UPDATE OF state ON project_operations
WHEN (NEW.state<>'succeeded') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_project_transition_insert AFTER INSERT ON projects
WHEN (NEW.state IN ('provisioning','resuming','pausing','deleting')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_project_transition_update AFTER UPDATE OF state ON projects
WHEN (NEW.state IN ('provisioning','resuming','pausing','deleting')) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_session_cleanup_insert AFTER INSERT ON sessions
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_session_cleanup_update AFTER UPDATE OF deletion_intent ON sessions
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_share_publication_insert AFTER INSERT ON shares
WHEN (NEW.state='pending' OR (NEW.state IN ('revoked','failed') AND NEW.cleaned=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_share_publication_update AFTER UPDATE OF state,cleaned ON shares
WHEN (NEW.state='pending' OR (NEW.state IN ('revoked','failed') AND NEW.cleaned=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_terminal_cleanup_insert AFTER INSERT ON terminals
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_terminal_cleanup_update AFTER UPDATE OF deletion_intent ON terminals
WHEN (NEW.deletion_intent=1) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_terminal_start_insert AFTER INSERT ON terminal_incarnations
WHEN (NEW.state='starting') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_terminal_start_update AFTER UPDATE OF state ON terminal_incarnations
WHEN (NEW.state='starting') AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_run_insert AFTER INSERT ON execution_runs
WHEN (NEW.state IN ('starting','running','stopping') OR (NEW.state='unknown' AND NEW.acknowledged=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_run_update AFTER UPDATE OF state,acknowledged ON execution_runs
WHEN (NEW.state IN ('starting','running','stopping') OR (NEW.state='unknown' AND NEW.acknowledged=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_share_start BEFORE INSERT ON shares
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_terminal_start BEFORE INSERT ON terminal_incarnations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_turn_dispatch BEFORE UPDATE OF queue_policy ON turns
WHEN NEW.queue_policy='released' AND NEW.queue_policy<>OLD.queue_policy
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_turn_insert BEFORE INSERT ON turns
WHEN NEW.queue_policy<>'held' AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER revoke_incarnation_services AFTER UPDATE OF state ON agent_incarnations
WHEN NEW.state<>'running' AND NEW.state<>'starting'
BEGIN UPDATE service_grants SET state='revoked' WHERE incarnation_id=NEW.id AND state='active'; END;

CREATE TRIGGER revoke_project_grants AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE conversation_grants SET revoked=1 WHERE session_id IN (SELECT id FROM sessions WHERE project_id=NEW.id); END;

CREATE TRIGGER revoke_project_links AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE shares SET state='revoked' WHERE project_id=NEW.id; END;

CREATE TRIGGER revoke_project_services AFTER UPDATE OF state ON projects
WHEN NEW.state<>'active'
BEGIN UPDATE service_grants SET state='revoked' WHERE project_id=NEW.id AND state='active'; END;

CREATE TRIGGER revoke_session_grants AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE conversation_grants SET revoked=1 WHERE session_id=NEW.id; END;

CREATE TRIGGER revoke_session_links AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE shares SET state='revoked' WHERE session_id=NEW.id; END;

CREATE TRIGGER revoke_session_services AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE service_grants SET state='revoked' WHERE session_id=NEW.id AND state='active'; END;

CREATE TRIGGER schedules_catalog_delete BEFORE DELETE ON schedules BEGIN
    UPDATE schedule_catalog SET revision=revision+1
    WHERE owner_id=(SELECT owner_id FROM projects WHERE id=OLD.project_id);
END;

CREATE TRIGGER schedules_catalog_insert AFTER INSERT ON schedules BEGIN
    INSERT INTO schedule_catalog(owner_id,revision)
    SELECT owner_id,1 FROM projects WHERE id=NEW.project_id
    ON CONFLICT(owner_id) DO UPDATE SET revision=revision+1;
END;

CREATE TRIGGER schedules_catalog_project_delete BEFORE DELETE ON projects BEGIN
    UPDATE schedule_catalog SET revision=revision+1 WHERE owner_id=OLD.owner_id;
END;

CREATE TRIGGER schedules_catalog_project_update AFTER UPDATE OF title,state,owner_id ON projects
WHEN OLD.title<>NEW.title OR OLD.owner_id<>NEW.owner_id OR
     (OLD.state IN ('deleting','deleted'))<>(NEW.state IN ('deleting','deleted')) BEGIN
    UPDATE schedule_catalog SET revision=revision+1 WHERE owner_id IN (OLD.owner_id,NEW.owner_id);
END;

CREATE TRIGGER schedules_catalog_update AFTER UPDATE ON schedules BEGIN
    UPDATE schedule_catalog SET revision=revision+1
    WHERE owner_id=(SELECT owner_id FROM projects WHERE id=NEW.project_id);
END;

CREATE TRIGGER sessions_parent_immutable BEFORE UPDATE OF project_id ON sessions
WHEN NEW.project_id<>OLD.project_id BEGIN SELECT RAISE(ABORT,'immutable Session parent'); END;

CREATE TRIGGER stop_project_schedules AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE schedules SET state='canceled',end_reason='project_deleted',next_at=NULL,retry_at=NULL,revision=revision+1 WHERE project_id=NEW.id AND state IN ('active','paused'); END;

CREATE TRIGGER stop_session_schedules AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE schedules SET state='canceled',end_reason='target_deleted',next_at=NULL,retry_at=NULL,revision=revision+1 WHERE session_id=NEW.id AND state IN ('active','paused'); END;

CREATE TRIGGER terminals_parent_immutable BEFORE UPDATE OF project_id ON terminals
WHEN NEW.project_id<>OLD.project_id BEGIN SELECT RAISE(ABORT,'immutable Terminal parent'); END;

CREATE TRIGGER turn_control_insert AFTER INSERT ON turns BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

CREATE TRIGGER turn_control_update AFTER UPDATE OF queue_policy,disposition ON turns WHEN NEW.queue_policy<>OLD.queue_policy OR NEW.disposition<>OLD.disposition BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

INSERT INTO application_release(id) VALUES(1);

CREATE TRIGGER prompt_policy_status AFTER UPDATE OF queue_policy,disposition ON turns BEGIN
 UPDATE turns SET prompt_revision=prompt_revision+1 WHERE id=NEW.id AND (NEW.queue_policy<>OLD.queue_policy OR NEW.disposition<>OLD.disposition);
END;
CREATE TRIGGER run_status_projection AFTER UPDATE OF state ON execution_runs BEGIN
 UPDATE turns SET updated_at=CASE WHEN NEW.settled_at<>'' THEN NEW.settled_at ELSE updated_at END
 WHERE id IN (SELECT prompt_id FROM input_deliveries WHERE run_id=NEW.id AND state='consumed');
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id AND NEW.state<>OLD.state;
END;
CREATE TRIGGER delivery_control_update AFTER UPDATE OF state ON input_deliveries WHEN OLD.state<>NEW.state BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

CREATE TABLE conversation_segments (
 session_id TEXT NOT NULL REFERENCES sessions(id),
 id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 prompt_id TEXT NOT NULL,
 position INTEGER NOT NULL,
 has_work INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'running',
 started_at TEXT NOT NULL,
 finished_at TEXT NOT NULL DEFAULT '',
 start_elapsed_ms INTEGER,
 elapsed_ms INTEGER,
 final_message_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(session_id,id)
);
CREATE INDEX conversation_segments_run ON conversation_segments(session_id,run_id,position DESC);
CREATE TABLE conversation_segment_inputs (
 session_id TEXT NOT NULL,
 prompt_id TEXT NOT NULL,
 segment_id TEXT NOT NULL,
 PRIMARY KEY(session_id,prompt_id),
 FOREIGN KEY(session_id,segment_id) REFERENCES conversation_segments(session_id,id)
);

CREATE TRIGGER delivery_binding BEFORE INSERT ON input_deliveries
WHEN NOT EXISTS(
 SELECT 1 FROM agent_commands c JOIN execution_runs r ON r.id=NEW.run_id
 WHERE c.id=NEW.command_id AND c.turn_id=NEW.prompt_id AND c.session_id=NEW.session_id
 AND c.incarnation_id=r.incarnation_id AND r.session_id=NEW.session_id
 AND ((NEW.channel='normal' AND c.kind='prompt' AND c.id=r.id) OR (NEW.channel='steer' AND c.kind='steer'))
)
BEGIN SELECT RAISE(ABORT,'invalid delivery binding'); END;

CREATE TRIGGER run_owner_fence AFTER UPDATE OF owner ON agent_incarnations BEGIN
 UPDATE execution_runs SET owner=NEW.owner WHERE incarnation_id=NEW.id AND state IN ('starting','running','stopping','unknown');
END;

CREATE TRIGGER delivery_consumption_immutable BEFORE UPDATE OF state ON input_deliveries
WHEN OLD.state IN ('consumed','handled') AND NEW.state<>OLD.state
BEGIN SELECT RAISE(ABORT,'consumption evidence is immutable'); END;

-- A small durable owner-scoped outbox for Scheduled. Independent of Project
-- retention, it also records deletion so removed resources invalidate clients.
CREATE TABLE schedule_event_frontiers (
 owner_id TEXT PRIMARY KEY,
 head INTEGER NOT NULL DEFAULT 0,
 retained_after INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE schedule_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 owner_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 schedule_id TEXT NOT NULL DEFAULT '',
 run_id TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL
);
CREATE TABLE schedule_run_sessions (
 session_id TEXT NOT NULL,
 schedule_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 run_count INTEGER NOT NULL,
 PRIMARY KEY(session_id,schedule_id)
);
CREATE TRIGGER schedule_run_session_insert AFTER INSERT ON schedule_runs BEGIN
 INSERT INTO schedule_run_sessions(session_id,schedule_id,project_id,run_count) VALUES(NEW.session_id,NEW.schedule_id,NEW.project_id,1)
 ON CONFLICT(session_id,schedule_id) DO UPDATE SET run_count=run_count+1;
END;
CREATE TRIGGER schedule_run_session_delete AFTER DELETE ON schedule_runs BEGIN
 UPDATE schedule_run_sessions SET run_count=run_count-1 WHERE session_id=OLD.session_id AND schedule_id=OLD.schedule_id;
 DELETE FROM schedule_run_sessions WHERE session_id=OLD.session_id AND schedule_id=OLD.schedule_id AND run_count=0;
END;
CREATE INDEX schedule_events_owner ON schedule_events(owner_id,id);
CREATE INDEX schedule_runs_session_schedule ON schedule_runs(session_id,schedule_id);
CREATE INDEX schedule_runs_history ON schedule_runs(project_id,schedule_id,created_at DESC,id DESC);
CREATE TRIGGER schedule_events_frontier AFTER INSERT ON schedule_events BEGIN
 INSERT INTO schedule_event_frontiers(owner_id,head) VALUES(NEW.owner_id,NEW.id)
 ON CONFLICT(owner_id) DO UPDATE SET head=NEW.id;
 DELETE FROM schedule_events WHERE owner_id=NEW.owner_id AND id<=coalesce(
  (SELECT id FROM schedule_events WHERE owner_id=NEW.owner_id ORDER BY id DESC LIMIT 1 OFFSET 2048),0);
END;
CREATE TRIGGER schedule_events_pruned BEFORE DELETE ON schedule_events BEGIN
 UPDATE schedule_event_frontiers SET retained_after=max(retained_after,OLD.id) WHERE owner_id=OLD.owner_id;
END;
CREATE TRIGGER schedule_hint_insert AFTER INSERT ON schedules BEGIN
 INSERT INTO schedule_events(owner_id,project_id,schedule_id,run_id,kind)
 SELECT owner_id,NEW.project_id,NEW.id,'','schedule.created' FROM projects WHERE id=NEW.project_id;
END;
CREATE TRIGGER schedule_hint_update AFTER UPDATE ON schedules BEGIN
 INSERT INTO schedule_events(owner_id,project_id,schedule_id,run_id,kind)
 SELECT owner_id,NEW.project_id,NEW.id,'',CASE WHEN NEW.state='deleted' AND OLD.state<>'deleted' THEN 'schedule.deleted' ELSE 'schedule.updated' END FROM projects WHERE id=NEW.project_id;
END;
CREATE TRIGGER schedule_hint_delete BEFORE DELETE ON schedules BEGIN
 INSERT INTO schedule_events(owner_id,project_id,schedule_id,run_id,kind)
 SELECT owner_id,OLD.project_id,OLD.id,'','schedule.deleted' FROM projects WHERE id=OLD.project_id;
END;
CREATE TRIGGER schedule_run_hint_insert AFTER INSERT ON schedule_runs BEGIN
 INSERT INTO schedule_events(owner_id,project_id,schedule_id,run_id,kind)
 SELECT owner_id,NEW.project_id,NEW.schedule_id,NEW.id,'run.created' FROM projects WHERE id=NEW.project_id;
END;
CREATE TRIGGER schedule_run_hint_delete BEFORE DELETE ON schedule_runs BEGIN
 INSERT INTO schedule_events(owner_id,project_id,schedule_id,run_id,kind)
 SELECT owner_id,OLD.project_id,OLD.schedule_id,OLD.id,'run.deleted' FROM projects WHERE id=OLD.project_id;
END;
CREATE TRIGGER schedule_run_hint_control AFTER UPDATE OF control_revision ON sessions
WHEN NEW.control_revision<>OLD.control_revision BEGIN
 INSERT INTO schedule_events(owner_id,project_id,schedule_id,kind)
 SELECT p.owner_id,NEW.project_id,r.schedule_id,'run.updated'
 FROM schedule_run_sessions r JOIN projects p ON p.id=NEW.project_id WHERE r.session_id=NEW.id;
END;
CREATE TRIGGER schedule_project_hint_update AFTER UPDATE OF title,state,owner_id ON projects
WHEN OLD.title<>NEW.title OR OLD.state<>NEW.state OR OLD.owner_id<>NEW.owner_id BEGIN
 INSERT INTO schedule_events(owner_id,project_id,kind) VALUES(OLD.owner_id,OLD.id,'schedule.updated');
 INSERT INTO schedule_events(owner_id,project_id,kind)
 SELECT NEW.owner_id,NEW.id,'schedule.updated' WHERE OLD.owner_id<>NEW.owner_id;
END;
CREATE TRIGGER schedule_project_hint_delete BEFORE DELETE ON projects BEGIN
 INSERT INTO schedule_events(owner_id,project_id,kind) VALUES(OLD.owner_id,OLD.id,'schedule.deleted');
END;

CREATE TRIGGER schedule_run_outcome_control AFTER UPDATE OF outcome ON execution_runs
WHEN NEW.outcome<>OLD.outcome AND NEW.state=OLD.state BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

CREATE VIEW schedule_check_attention AS
 SELECT 'schedule/'||s.id AS id,p.owner_id AS audience,s.project_id,
 coalesce((SELECT r.session_id FROM schedule_runs r WHERE r.schedule_id=s.id ORDER BY r.created_at DESC,r.id DESC LIMIT 1),s.session_id) AS session_id,
 'Scheduled check stopped: '||s.end_reason AS title,
 'No confirmed result before the check budget ended. Inspect the task and its runs.' AS body,s.updated_at AS created_at
 FROM schedules s JOIN projects p ON p.id=s.project_id
 WHERE p.state NOT IN ('deleting','deleted') AND s.state='exhausted' AND s.end_reason IN ('count','deadline') AND s.completed_by_run_id IS NULL AND s.max_occurrences IS NOT NULL AND s.notify=1
 AND NOT EXISTS(SELECT 1 FROM schedule_runs r JOIN turns t ON t.id=r.turn_id WHERE r.schedule_id=s.id AND (CASE t.disposition
 WHEN 'pending' THEN CASE t.queue_policy WHEN 'held' THEN 'held' ELSE 'queued' END
 WHEN 'claimed' THEN coalesce((SELECT CASE r.state WHEN 'settled' THEN r.outcome ELSE r.state END
  FROM execution_runs r WHERE r.prompt_id=t.id ORDER BY r.started_at DESC,r.id DESC LIMIT 1),'claimed')
 WHEN 'canceled' THEN 'canceled'
 WHEN 'handled' THEN 'handled'
 ELSE coalesce((SELECT CASE r.state WHEN 'settled' THEN r.outcome ELSE r.state END
  FROM input_deliveries d JOIN execution_runs r ON r.id=d.run_id
  WHERE d.prompt_id=t.id AND d.state='consumed' ORDER BY d.acceptance_sequence DESC LIMIT 1),'consumed') END) IN ('queued','held','claimed','starting','running','stopping','unknown','consumed'));
CREATE TRIGGER schedule_check_attention_definition AFTER UPDATE OF state ON schedules BEGIN
 INSERT OR IGNORE INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT * FROM schedule_check_attention WHERE id='schedule/'||NEW.id;
END;
CREATE TRIGGER schedule_check_attention_execution AFTER UPDATE OF state,outcome ON execution_runs
WHEN NEW.state IN ('settled','failed') BEGIN
 INSERT OR IGNORE INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT * FROM schedule_check_attention WHERE id IN (SELECT 'schedule/'||sr.schedule_id FROM schedule_runs sr WHERE sr.turn_id=NEW.prompt_id OR EXISTS(SELECT 1 FROM input_deliveries d WHERE d.prompt_id=sr.turn_id AND d.run_id=NEW.id AND d.state='consumed'));
END;
CREATE TRIGGER schedule_check_attention_disposition AFTER UPDATE OF disposition ON turns
WHEN NEW.disposition IN ('handled','canceled') BEGIN
 INSERT OR IGNORE INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT * FROM schedule_check_attention WHERE id IN (SELECT 'schedule/'||schedule_id FROM schedule_runs WHERE turn_id=NEW.id);
END;
