-- Version 2: disposable conversation display state. Raw execution records,
-- message evidence and command outcomes remain authoritative and are retained.
-- Source identities survive raw-table VACUUM/rebuild. Migration copies metadata
-- once in ingestion order; subsequent ingestion appends in the same transaction.
CREATE TABLE conversation_sources (
 source_id INTEGER PRIMARY KEY AUTOINCREMENT,
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 incarnation_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 UNIQUE(incarnation_id,sequence)
);
CREATE INDEX conversation_sources_session ON conversation_sources(session_id,source_id);
INSERT INTO conversation_sources(session_id,incarnation_id,sequence)
 SELECT i.session_id,r.incarnation_id,r.sequence FROM agent_records r
 JOIN agent_incarnations i ON i.id=r.incarnation_id ORDER BY r.rowid;
CREATE TRIGGER conversation_source_ingest AFTER INSERT ON agent_records BEGIN
 INSERT INTO conversation_sources(session_id,incarnation_id,sequence)
 SELECT session_id,NEW.incarnation_id,NEW.sequence FROM agent_incarnations WHERE id=NEW.incarnation_id;
END;
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
CREATE TABLE conversation_items (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 position INTEGER NOT NULL,
 block_index INTEGER NOT NULL,
 item_revision INTEGER NOT NULL,
 source_key TEXT NOT NULL,
 data TEXT NOT NULL,
 PRIMARY KEY(session_id,id)
);
CREATE INDEX conversation_items_order ON conversation_items(session_id,position,block_index,id);
CREATE INDEX conversation_items_turn ON conversation_items(session_id,turn_id,position,block_index,id);
CREATE INDEX conversation_items_source ON conversation_items(session_id,source_key);
CREATE TABLE conversation_changes (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 ordinal INTEGER NOT NULL,
 item_id TEXT NOT NULL,
 operation TEXT NOT NULL CHECK(operation IN ('upsert','delete')),
 data TEXT NOT NULL,
 PRIMARY KEY(session_id,revision,ordinal)
);
CREATE TABLE conversation_details (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 item_id TEXT NOT NULL,
 item_revision INTEGER NOT NULL,
 content TEXT NOT NULL,
 PRIMARY KEY(session_id,item_id,item_revision)
);
CREATE TABLE conversation_turns (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 turn_id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 data TEXT NOT NULL,
 PRIMARY KEY(session_id,turn_id)
);
CREATE INDEX conversation_turns_order ON conversation_turns(session_id,sequence);
