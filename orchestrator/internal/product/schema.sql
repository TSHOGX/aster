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

CREATE TABLE auth_sessions (
 token_digest TEXT PRIMARY KEY,
 actor TEXT NOT NULL,
 credential_digest TEXT NOT NULL,
 expires_at TEXT NOT NULL
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
 message_cursor INTEGER NOT NULL DEFAULT 0,
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
 reader_id TEXT NOT NULL CHECK(reader_id<>''),
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
 data TEXT NOT NULL, turn_sequence INTEGER NOT NULL DEFAULT 0, message_id TEXT NOT NULL DEFAULT '',
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
 turn_id TEXT PRIMARY KEY REFERENCES turns(id),
 incarnation_id TEXT NOT NULL REFERENCES agent_incarnations(id),
 path TEXT NOT NULL,
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
 actor TEXT NOT NULL,
 PRIMARY KEY(notification_id,actor)
);

CREATE TABLE notifications (
 id TEXT PRIMARY KEY,
 audience TEXT,
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
 owner_id TEXT NOT NULL CHECK(owner_id<>''),
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
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1)),
 next_at TEXT,
 revision INTEGER NOT NULL DEFAULT 1,
 creation_digest TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id)
);

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
 state TEXT NOT NULL CHECK(state IN ('held','queued','starting','running','stopping','completed','failed','canceled','unknown')),
 acknowledged INTEGER NOT NULL DEFAULT 0 CHECK(acknowledged IN (0,1)),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(project_id,session_id) REFERENCES sessions(project_id,id),
 UNIQUE(session_id,request_id),
 UNIQUE(session_id,sequence),
 UNIQUE(project_id,session_id,id)
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

CREATE UNIQUE INDEX agent_incarnation_live ON agent_incarnations(session_id) WHERE state IN ('starting','running','stopping','unknown');

CREATE UNIQUE INDEX agent_prompt_turn ON agent_commands(turn_id) WHERE kind='prompt';

CREATE INDEX auth_sessions_expiry ON auth_sessions(expires_at);

CREATE INDEX conversation_changes_detail ON conversation_changes(session_id,item_id,revision);

CREATE INDEX conversation_items_message ON conversation_items(session_id,message_id);

CREATE INDEX conversation_items_order ON conversation_items(session_id,turn_sequence,position,block_index,id);

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

CREATE INDEX release_native_capture_pending ON native_captures(turn_id) WHERE state='guest';

CREATE INDEX release_project_operation_pending ON project_operations(id) WHERE state<>'succeeded';

CREATE INDEX release_project_transition_pending ON projects(id) WHERE state IN ('provisioning','resuming','pausing','deleting');

CREATE INDEX release_session_cleanup_pending ON sessions(id) WHERE deletion_intent=1;

CREATE INDEX release_share_publication_pending ON shares(id) WHERE state='pending' OR (state IN ('revoked','failed') AND cleaned=0);

CREATE INDEX release_terminal_cleanup_pending ON terminals(id) WHERE deletion_intent=1;

CREATE INDEX release_terminal_start_pending ON terminal_incarnations(id) WHERE state='starting';

CREATE INDEX release_turn_pending ON turns(id) WHERE state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0);

CREATE INDEX schedule_runs_project ON schedule_runs(project_id,created_at,id);

CREATE INDEX schedules_due ON schedules(enabled,deleted,next_at,project_id);

CREATE INDEX schedules_overview_order ON schedules(
    deleted,
    CASE WHEN enabled=0 THEN 2 WHEN cron='' THEN 1 ELSE 0 END,
    coalesce(next_at,''),id
);

CREATE INDEX service_grants_live ON service_grants(project_id,state,expires_at);

CREATE UNIQUE INDEX service_grants_one_live ON service_grants(incarnation_id,audience) WHERE state='active';

CREATE INDEX sessions_project ON sessions(project_id,created_at,id);

CREATE INDEX shares_cleanup ON shares(state,cleaned,created_at);

CREATE UNIQUE INDEX terminal_incarnation_live ON terminal_incarnations(terminal_id) WHERE state IN ('starting','running');

CREATE INDEX terminal_incarnations_project ON terminal_incarnations(project_id,state);

CREATE INDEX terminals_project ON terminals(project_id,created_at,id);

CREATE UNIQUE INDEX turns_one_active ON turns(session_id) WHERE state IN ('starting','running','stopping');

CREATE INDEX turns_pending ON turns(project_id,state,session_id,sequence);

CREATE VIEW owner_handoff_blockers AS
SELECT b.kind,b.id FROM release_blockers b WHERE
 (b.kind NOT IN ('turn','model_request'))
 OR (b.kind='turn' AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t WHERE t.id=b.id))
 OR (b.kind='model_request' AND NOT EXISTS(
  SELECT 1 FROM service_request_slots r JOIN owner_transferable_turns t ON t.id=r.turn_id
  JOIN service_grants g ON g.id=r.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=r.audience
   AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now')
  WHERE r.audience||':'||r.slot=b.id AND r.closed=0
 ));

CREATE VIEW owner_transferable_turns AS
SELECT t.id,c.incarnation_id FROM turns t INDEXED BY release_turn_pending
JOIN agent_commands c INDEXED BY agent_prompt_turn ON c.turn_id=t.id AND c.kind='prompt' AND c.state='applied'
JOIN agent_incarnations i ON i.id=c.incarnation_id AND i.session_id=t.session_id AND i.state='running'
JOIN sessions s ON s.id=t.session_id AND s.deletion_intent=0 AND s.state='running'
JOIN projects p ON p.id=t.project_id AND p.state='active'
WHERE (t.state IN ('starting','running','stopping') OR (t.state='unknown' AND t.acknowledged=0)) AND t.state='running';

CREATE VIEW release_blockers AS
SELECT 'turn' AS kind,id AS id FROM turns INDEXED BY release_turn_pending WHERE state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0)
UNION ALL SELECT 'command' AS kind,id AS id FROM agent_commands INDEXED BY release_command_pending
WHERE state IN ('pending','sending','dispatching','unknown') AND (state<>'unknown' OR NOT EXISTS(
 SELECT 1 FROM turns t JOIN agent_incarnations i ON i.id=agent_commands.incarnation_id
 WHERE t.id=agent_commands.turn_id AND t.acknowledged=1 AND i.state='exited'
 AND i.cleanup_state='settled' AND i.recovery_state NOT IN ('pending','replaying')
))
UNION ALL SELECT 'activity' AS kind,id AS id FROM project_activity INDEXED BY release_activity_pending WHERE state<>'complete'
UNION ALL SELECT 'agent_transition' AS kind,id AS id FROM agent_incarnations INDEXED BY release_agent_transition_pending
WHERE (state IN ('starting','stopping','unknown') OR recovery_state IN ('pending','replaying','failed'))
 AND (state IN ('starting','stopping','unknown') OR recovery_state<>'failed' OR EXISTS(
 SELECT 1 FROM turns t WHERE t.session_id=agent_incarnations.session_id AND t.state='unknown' AND t.acknowledged=0
))
UNION ALL SELECT 'terminal_start' AS kind,id AS id FROM terminal_incarnations INDEXED BY release_terminal_start_pending WHERE state='starting'
UNION ALL SELECT 'project_operation' AS kind,id AS id FROM project_operations INDEXED BY release_project_operation_pending WHERE state<>'succeeded'
UNION ALL SELECT 'project_transition' AS kind,id AS id FROM projects INDEXED BY release_project_transition_pending WHERE state IN ('provisioning','resuming','pausing','deleting')
UNION ALL SELECT 'file_write' AS kind,id AS id FROM file_operations INDEXED BY release_file_write_pending WHERE settled=0
UNION ALL SELECT 'session_cleanup' AS kind,id AS id FROM sessions INDEXED BY release_session_cleanup_pending WHERE deletion_intent=1
UNION ALL SELECT 'terminal_cleanup' AS kind,id AS id FROM terminals INDEXED BY release_terminal_cleanup_pending WHERE deletion_intent=1
UNION ALL SELECT 'content_publication' AS kind,revision_digest AS id FROM content_publications INDEXED BY release_content_publication_pending WHERE stage NOT IN ('published','failed')
UNION ALL SELECT 'native_capture' AS kind,turn_id AS id FROM native_captures INDEXED BY release_native_capture_pending WHERE state='guest'
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

CREATE TRIGGER notify_turn_outcome AFTER UPDATE OF state ON turns
WHEN NEW.state IN ('completed','failed','canceled','unknown') AND NEW.state<>OLD.state
BEGIN
 INSERT OR IGNORE INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT 'turn/'||NEW.id,owner_id,NEW.project_id,NEW.session_id,'Agent turn '||NEW.state,'Open the conversation to inspect the outcome.',NEW.updated_at FROM projects WHERE id=NEW.project_id AND state NOT IN ('deleting','deleted')
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
 AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t JOIN service_grants g ON g.id=NEW.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=NEW.audience AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE t.id=NEW.turn_id)
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_request_update AFTER UPDATE ON service_request_slots
WHEN NEW.closed=0
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
 AND NOT EXISTS(SELECT 1 FROM owner_transferable_turns t JOIN service_grants g ON g.id=NEW.grant_id AND g.incarnation_id=t.incarnation_id AND g.audience=NEW.audience AND g.state='active' AND g.expires_at>strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE t.id=NEW.turn_id)
BEGIN UPDATE application_release SET phase='draining',reason='retained_authority_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_session_changed AFTER UPDATE OF state ON sessions
WHEN OLD.state='running' AND NEW.state<>'running'
 AND EXISTS(SELECT 1 FROM turns WHERE session_id=NEW.id AND state='running')
 AND EXISTS(SELECT 1 FROM application_release WHERE id=1 AND mode='owner' AND phase='ready')
BEGIN UPDATE application_release SET phase='draining',reason='retained_work_changed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_owner_turn_finished AFTER UPDATE OF state ON turns
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

CREATE TRIGGER release_ready_turn_insert AFTER INSERT ON turns
WHEN (NEW.state IN ('starting','running','stopping') OR (NEW.state='unknown' AND NEW.acknowledged=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_ready_turn_update AFTER UPDATE OF state,acknowledged ON turns
WHEN (NEW.state IN ('starting','running','stopping') OR (NEW.state='unknown' AND NEW.acknowledged=0)) AND (SELECT phase FROM application_release WHERE id=1)='ready'
BEGIN UPDATE application_release SET phase='draining',reason='work_resumed',updated_at=strftime('%Y-%m-%dT%H:%M:%f000000Z','now') WHERE id=1 AND phase='ready'; END;

CREATE TRIGGER release_share_start BEFORE INSERT ON shares
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_terminal_start BEFORE INSERT ON terminal_incarnations
WHEN (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_turn_dispatch BEFORE UPDATE OF state ON turns
WHEN NEW.state IN ('queued','starting') AND NEW.state<>OLD.state
 AND (SELECT phase FROM application_release WHERE id=1)<>'open'
BEGIN SELECT RAISE(ABORT,'application_admission_closed'); END;

CREATE TRIGGER release_turn_insert BEFORE INSERT ON turns
WHEN NEW.state<>'held' AND (SELECT phase FROM application_release WHERE id=1)<>'open'
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
BEGIN UPDATE schedules SET enabled=0,next_at=NULL,revision=revision+1 WHERE project_id=NEW.id AND enabled=1; END;

CREATE TRIGGER stop_session_schedules AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE schedules SET enabled=0,next_at=NULL,revision=revision+1 WHERE session_id=NEW.id AND enabled=1; END;

CREATE TRIGGER terminals_parent_immutable BEFORE UPDATE OF project_id ON terminals
WHEN NEW.project_id<>OLD.project_id BEGIN SELECT RAISE(ABORT,'immutable Terminal parent'); END;

CREATE TRIGGER turn_control_insert AFTER INSERT ON turns BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

CREATE TRIGGER turn_control_update AFTER UPDATE OF state ON turns WHEN NEW.state<>OLD.state BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;

INSERT INTO application_release(id) VALUES(1);
