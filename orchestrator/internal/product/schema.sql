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

CREATE TABLE sessions (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL REFERENCES projects(id),
 title TEXT NOT NULL,
 creation_digest TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'idle' CHECK(state IN ('idle','starting','running','stopping','blocked','deleting','deleted')),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 failure TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(project_id,id)
);
CREATE INDEX sessions_project ON sessions(project_id,created_at,id);
CREATE TRIGGER sessions_parent_immutable BEFORE UPDATE OF project_id ON sessions
WHEN NEW.project_id<>OLD.project_id BEGIN SELECT RAISE(ABORT,'immutable Session parent'); END;

CREATE TABLE terminals (
 id TEXT PRIMARY KEY CHECK(id<>''),
 project_id TEXT NOT NULL REFERENCES projects(id),
 title TEXT NOT NULL,
 creation_digest TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'created' CHECK(state IN ('created','starting','running','stopping','exited','lost','deleting','deleted')),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 rows INTEGER NOT NULL CHECK(rows BETWEEN 1 AND 500),
 cols INTEGER NOT NULL CHECK(cols BETWEEN 1 AND 1000),
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
CREATE TRIGGER revoke_session_grants AFTER UPDATE OF state ON sessions
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE conversation_grants SET revoked=1 WHERE session_id=NEW.id; END;
CREATE TRIGGER revoke_project_grants AFTER UPDATE OF state ON projects
WHEN NEW.state IN ('deleting','deleted')
BEGIN UPDATE conversation_grants SET revoked=1 WHERE session_id IN (SELECT id FROM sessions WHERE project_id=NEW.id); END;
