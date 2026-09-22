-- Version 3: indexed display ordering and bounded reducer work discovery.
ALTER TABLE conversation_items ADD COLUMN turn_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_items ADD COLUMN message_id TEXT NOT NULL DEFAULT '';
DROP INDEX conversation_items_order;
CREATE INDEX conversation_items_order ON conversation_items(session_id,turn_sequence,position,block_index,id);
CREATE INDEX conversation_items_message ON conversation_items(session_id,message_id);
CREATE TABLE conversation_message_groups (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 incarnation_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 start_sequence INTEGER NOT NULL,
 position INTEGER NOT NULL,
 ended INTEGER NOT NULL DEFAULT 0 CHECK(ended IN (0,1)),
 PRIMARY KEY(session_id,incarnation_id,turn_id)
);
CREATE TABLE conversation_dirty_turns (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 turn_id TEXT NOT NULL,
 PRIMARY KEY(session_id,turn_id)
);
CREATE TABLE conversation_pending_sessions (
 session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE
);
INSERT INTO conversation_pending_sessions SELECT id FROM sessions WHERE deletion_intent<2;
CREATE TRIGGER conversation_session_insert AFTER INSERT ON sessions BEGIN
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.id);
END;
CREATE TRIGGER conversation_source_pending AFTER INSERT ON conversation_sources BEGIN
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;
INSERT INTO conversation_dirty_turns SELECT session_id,id FROM turns;
CREATE TRIGGER conversation_turn_insert AFTER INSERT ON turns BEGIN
 INSERT OR IGNORE INTO conversation_dirty_turns VALUES(NEW.session_id,NEW.id);
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;
CREATE TRIGGER conversation_turn_update AFTER UPDATE ON turns BEGIN
 INSERT OR IGNORE INTO conversation_dirty_turns VALUES(NEW.session_id,NEW.id);
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;
CREATE TRIGGER conversation_message_insert AFTER INSERT ON messages BEGIN
 INSERT OR IGNORE INTO conversation_dirty_turns VALUES(NEW.session_id,NEW.turn_id);
 INSERT OR IGNORE INTO conversation_pending_sessions VALUES(NEW.session_id);
END;
