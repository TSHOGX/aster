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
CREATE INDEX projects_owner ON projects(owner_id,created_at,id);

-- state describes how the child is executing; deletion_intent describes whether
-- the user asked for it to go away. They are separate columns on purpose: a
-- blocked Session stays visibly blocked while its cleanup runs, and settling a
-- child never erases the evidence of how it ended.
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
CREATE INDEX sessions_project ON sessions(project_id,created_at,id);
CREATE TRIGGER sessions_parent_immutable BEFORE UPDATE OF project_id ON sessions
WHEN NEW.project_id<>OLD.project_id BEGIN SELECT RAISE(ABORT,'immutable Session parent'); END;

-- rows/cols are the Terminal's persistent dimensions: a later incarnation starts
-- at the size the user last chose. exit_code and state describe the most recent
-- incarnation's outcome and survive deletion.
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
CREATE INDEX terminals_project ON terminals(project_id,created_at,id);
CREATE TRIGGER terminals_parent_immutable BEFORE UPDATE OF project_id ON terminals
WHEN NEW.project_id<>OLD.project_id BEGIN SELECT RAISE(ABORT,'immutable Terminal parent'); END;

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
CREATE INDEX turns_pending ON turns(project_id,state,session_id,sequence);
CREATE UNIQUE INDEX turns_one_active ON turns(session_id) WHERE state IN ('starting','running','stopping');

-- Product projections only. Agent records and terminal byte replay have their
-- own contracts; neither is smuggled into this outbox.
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
CREATE INDEX events_project ON events(project_id,id);

CREATE TABLE conversation_grants (
 session_id TEXT NOT NULL REFERENCES sessions(id),
 reader_id TEXT NOT NULL CHECK(reader_id<>''),
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)),
 PRIMARY KEY(session_id,reader_id)
);
CREATE TRIGGER revoke_session_grants AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE conversation_grants SET revoked=1 WHERE session_id=NEW.id; END;
CREATE TRIGGER revoke_project_grants AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE conversation_grants SET revoked=1 WHERE session_id IN (SELECT id FROM sessions WHERE project_id=NEW.id); END;

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
);
CREATE UNIQUE INDEX project_sandbox_unique ON project_resources(store_id,sandbox_id) WHERE sandbox_id<>'';
CREATE UNIQUE INDEX project_workspace_unique ON project_resources(store_id,workspace_volume) WHERE workspace_volume<>'';
CREATE UNIQUE INDEX project_runtime_unique ON project_resources(store_id,runtime_volume) WHERE runtime_volume<>'';

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
CREATE UNIQUE INDEX project_operation_lane ON project_operations(project_id) WHERE state<>'succeeded';
CREATE INDEX project_operations_pending ON project_operations(state,attempted_at,project_id);

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
CREATE INDEX project_activity_pending ON project_activity(project_id,state);

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
CREATE UNIQUE INDEX agent_incarnation_live ON agent_incarnations(session_id) WHERE state IN ('starting','running','stopping','unknown');
-- A Terminal's PTY process record, the exact analogue of agent_incarnations. A
-- Terminal row alone owns no process and pins nothing; a live incarnation here is
-- what keeps the Project awake, including while silent and browser-detached.
--
-- exit_code is written only from the process stream's terminal frame. 'lost'
-- means the placement that held this PTY is gone: the process did not exit, its
-- outcome is simply unobservable, so no code is recorded.
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
CREATE UNIQUE INDEX terminal_incarnation_live ON terminal_incarnations(terminal_id) WHERE state IN ('starting','running');
CREATE INDEX terminal_incarnations_project ON terminal_incarnations(project_id,state);

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
CREATE UNIQUE INDEX agent_prompt_turn ON agent_commands(turn_id) WHERE kind='prompt';
CREATE INDEX agent_commands_pending ON agent_commands(incarnation_id,state,created_at);
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
CREATE INDEX messages_project ON messages(project_id,session_id,sequence);
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

-- A grant is scoped to a Session's agent incarnation and one declared audience,
-- never to a turn. Turn attribution is resolved by the host at each request, so
-- a stale credential cannot inherit a successor turn's authority and a guest
-- never holds a per-turn secret.
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
CREATE INDEX service_grants_live ON service_grants(project_id,state,expires_at);
CREATE UNIQUE INDEX service_grants_one_live ON service_grants(incarnation_id,audience) WHERE state='active';

CREATE TRIGGER revoke_project_services AFTER UPDATE OF state ON projects
WHEN NEW.state<>'active'
BEGIN UPDATE service_grants SET state='revoked' WHERE project_id=NEW.id AND state='active'; END;
CREATE TRIGGER revoke_incarnation_services AFTER UPDATE OF state ON agent_incarnations
WHEN NEW.state<>'running' AND NEW.state<>'starting'
BEGIN UPDATE service_grants SET state='revoked' WHERE incarnation_id=NEW.id AND state='active'; END;
CREATE TRIGGER revoke_session_services AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE service_grants SET state='revoked' WHERE session_id=NEW.id AND state='active'; END;

-- File operation outcomes are independent from helper liveness. An unknown
-- write is never replayed, while confirmed helper termination releases activity.
CREATE TABLE file_operations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 request TEXT NOT NULL,
 store_id TEXT NOT NULL,
 sandbox_id TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>0),
 handle TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('running','succeeded','failed','unknown')),
 settled INTEGER NOT NULL DEFAULT 0 CHECK(settled IN (0,1)),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX file_operations_pending ON file_operations(settled,updated_at,project_id);

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
CREATE INDEX schedules_due ON schedules(enabled,deleted,next_at,project_id);
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
CREATE INDEX schedule_runs_project ON schedule_runs(project_id,created_at,id);
CREATE TRIGGER stop_project_schedules AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE schedules SET enabled=0,next_at=NULL,revision=revision+1 WHERE project_id=NEW.id AND enabled=1; END;
CREATE TRIGGER stop_session_schedules AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE schedules SET enabled=0,next_at=NULL,revision=revision+1 WHERE session_id=NEW.id AND enabled=1; END;

CREATE TABLE notifications (
 id TEXT PRIMARY KEY,
 audience TEXT,
 project_id TEXT REFERENCES projects(id),
 session_id TEXT REFERENCES sessions(id),
 title TEXT NOT NULL,
 body TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX notifications_audience ON notifications(audience,created_at,id);
CREATE TABLE notification_reads (
 notification_id TEXT NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
 actor TEXT NOT NULL,
 PRIMARY KEY(notification_id,actor)
);
CREATE TRIGGER notify_turn_outcome AFTER UPDATE OF state ON turns
WHEN NEW.state IN ('completed','failed','canceled','unknown') AND NEW.state<>OLD.state
BEGIN
 INSERT OR IGNORE INTO notifications(id,audience,project_id,session_id,title,body,created_at)
 SELECT 'turn/'||NEW.id,owner_id,NEW.project_id,NEW.session_id,'Agent turn '||NEW.state,'Open the conversation to inspect the outcome.',NEW.updated_at FROM projects WHERE id=NEW.project_id AND state NOT IN ('deleting','deleted')
 ON CONFLICT(id) DO UPDATE SET title=excluded.title,body=excluded.body,created_at=excluded.created_at;
 DELETE FROM notification_reads WHERE notification_id='turn/'||NEW.id;
END;

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
CREATE INDEX shares_cleanup ON shares(state,cleaned,created_at);
CREATE TABLE share_files (
 share_id TEXT NOT NULL REFERENCES shares(id),
 path TEXT NOT NULL,
 digest TEXT NOT NULL,
 size INTEGER NOT NULL CHECK(size>=0),
 PRIMARY KEY(share_id,path)
);
CREATE TRIGGER revoke_session_links AFTER UPDATE OF deletion_intent ON sessions
WHEN NEW.deletion_intent>0
BEGIN UPDATE shares SET state='revoked' WHERE session_id=NEW.id; END;
CREATE TRIGGER revoke_project_links AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE shares SET state='revoked' WHERE project_id=NEW.id; END;

CREATE VIRTUAL TABLE message_search USING fts5(content,content='messages',content_rowid='rowid');
CREATE TRIGGER index_message_insert AFTER INSERT ON messages BEGIN
 INSERT INTO message_search(rowid,content) VALUES(NEW.rowid,NEW.content);
END;
CREATE TRIGGER index_message_delete AFTER DELETE ON messages BEGIN
 INSERT INTO message_search(message_search,rowid,content) VALUES('delete',OLD.rowid,OLD.content);
END;

-- Login sessions are authentication records, unrelated to Agent Sessions.
CREATE TABLE auth_sessions (
 token_digest TEXT PRIMARY KEY,
 actor TEXT NOT NULL,
 credential_digest TEXT NOT NULL,
 expires_at TEXT NOT NULL
);
CREATE INDEX auth_sessions_expiry ON auth_sessions(expires_at);

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

-- Streaming projections must not invalidate a user's execution-control intent.
CREATE TRIGGER turn_control_insert AFTER INSERT ON turns BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;
CREATE TRIGGER turn_control_update AFTER UPDATE OF state ON turns WHEN NEW.state<>OLD.state BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;
CREATE TRIGGER command_control_insert AFTER INSERT ON agent_commands BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;
CREATE TRIGGER command_control_update AFTER UPDATE OF state ON agent_commands WHEN NEW.state<>OLD.state BEGIN
 UPDATE sessions SET control_revision=control_revision+1 WHERE id=NEW.session_id;
END;


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
